package mcpclient

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

// Exchange is one HTTP request and its corresponding response, as seen on the wire.
type Exchange struct {
	Method          string              `json:"method"`
	URL             string              `json:"url"`
	RequestHeaders  map[string][]string `json:"requestHeaders,omitempty"`
	RequestBody     json.RawMessage     `json:"requestBody,omitempty"`
	Status          int                 `json:"status"`
	ResponseHeaders map[string][]string `json:"responseHeaders,omitempty"`
	ResponseBody    json.RawMessage     `json:"responseBody,omitempty"`
}

// Capture holds exchanges made by the HTTP transport. Stdio has no HTTP wire to capture.
type Capture struct {
	mu        sync.Mutex
	exchanges []*Exchange
}

func NewCapture() *Capture { return &Capture{} }

// Exchanges returns a copy suitable for serialisation after the session has finished.
func (c *Capture) Exchanges() []Exchange {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Exchange, len(c.exchanges))
	for i, e := range c.exchanges {
		out[i] = *e
		out[i].RequestBody = append([]byte(nil), e.RequestBody...)
		out[i].ResponseBody = append([]byte(nil), e.ResponseBody...)
	}
	return out
}

type httpClientForCapture struct{ capture *Capture }

func (c *httpClientForCapture) Client() *http.Client {
	return &http.Client{Transport: captureTransport{base: http.DefaultTransport, capture: c.capture}}
}

type captureTransport struct {
	base    http.RoundTripper
	capture *Capture
}

func (t captureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var requestBody []byte
	if req.Body != nil {
		var err error
		requestBody, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(requestBody))
	}

	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	exchange := &Exchange{
		Method:          req.Method,
		URL:             req.URL.String(),
		RequestHeaders:  req.Header.Clone(),
		RequestBody:     requestBody,
		Status:          resp.StatusCode,
		ResponseHeaders: resp.Header.Clone(),
	}
	t.capture.mu.Lock()
	t.capture.exchanges = append(t.capture.exchanges, exchange)
	t.capture.mu.Unlock()
	// The body is bounded before anything reads it, so the limit protects BOTH our capture
	// buffer and the SDK's decoder: a public server we do not control should not be able to
	// decide how much memory this process takes.
	resp.Body = &capturedBody{ReadCloser: newLimitedBody(resp.Body), exchange: exchange, capture: t.capture}
	return resp, nil
}

// MaxResponseBytes caps a single MCP response. DeepWiki's tools/list is ~1.6 KB and the
// SDK's example server ~8.7 KB, so eight megabytes is four orders of magnitude of room.
const MaxResponseBytes = 8 << 20

// ErrResponseTooLarge is returned instead of buffering an unbounded response body.
var ErrResponseTooLarge = errors.New("ответ сервера больше допустимого размера")

type limitedBody struct {
	io.ReadCloser
	left int64
}

func newLimitedBody(body io.ReadCloser) io.ReadCloser {
	return &limitedBody{ReadCloser: body, left: MaxResponseBytes}
}

func (b *limitedBody) Read(p []byte) (int, error) {
	if b.left <= 0 {
		return 0, fmt.Errorf("%w (%d байт)", ErrResponseTooLarge, int64(MaxResponseBytes))
	}
	if int64(len(p)) > b.left {
		p = p[:b.left]
	}
	n, err := b.ReadCloser.Read(p)
	b.left -= int64(n)
	return n, err
}

type capturedBody struct {
	io.ReadCloser
	exchange *Exchange
	capture  *Capture
	body     bytes.Buffer
}

func (b *capturedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		_, _ = b.body.Write(p[:n])
	}
	if err == io.EOF {
		b.save()
	}
	return n, err
}

func (b *capturedBody) Close() error {
	b.save()
	return b.ReadCloser.Close()
}

func (b *capturedBody) save() {
	b.capture.mu.Lock()
	b.exchange.ResponseBody = jsonRPCBody(b.body.Bytes())
	b.capture.mu.Unlock()
}

func jsonRPCBody(body []byte) json.RawMessage {
	body = bytes.TrimSpace(body)
	if json.Valid(body) {
		return append([]byte(nil), body...)
	}
	for _, line := range strings.Split(string(body), "\n") {
		if data, ok := strings.CutPrefix(line, "data:"); ok {
			data = strings.TrimSpace(data)
			if json.Valid([]byte(data)) {
				return json.RawMessage(data)
			}
		}
	}
	quoted, _ := json.Marshal(string(body))
	return quoted
}
