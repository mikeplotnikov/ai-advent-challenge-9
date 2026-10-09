package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func localClient(endpoint string) (*http.Client, string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, "", err
	}
	host := u.Hostname()
	if host == "localhost" {
		host = "127.0.0.1"
	}
	ip := net.ParseIP(host)
	if u.Scheme != "http" || ip == nil || !ip.IsLoopback() || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, "", fmt.Errorf("only an HTTP loopback Ollama endpoint is allowed")
	}
	port := u.Port()
	if port == "" {
		port = "80"
	}
	u.Host = net.JoinHostPort(host, port)
	u.Path = ""
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &http.Client{Transport: transport, Timeout: 3 * time.Minute,
		CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("redirects are not allowed") }}, u.String(), nil
}

func (a *app) call(ctx context.Context, path string, payload any, target any) (json.RawMessage, error) {
	method := http.MethodGet
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(data)
		method = http.MethodPost
	}
	req, err := http.NewRequestWithContext(ctx, method, a.endpoint+path, body)
	if err != nil {
		return nil, err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Ollama недоступна: %w", err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, (2<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 2<<20 {
		return nil, fmt.Errorf("ответ Ollama превышает 2 MiB")
	}
	if res.StatusCode != 200 {
		var envelope struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &envelope)
		return nil, fmt.Errorf("Ollama %s: HTTP %d %s", path, res.StatusCode, envelope.Error)
	}
	if err := json.Unmarshal(data, target); err != nil {
		return nil, fmt.Errorf("Ollama вернула некорректный JSON: %w", err)
	}
	return data, nil
}

func (a *app) modelInfo(ctx context.Context, model, capability string) (identity, error) {
	id := identity{Model: model, Endpoint: a.endpoint}
	if strings.Contains(strings.ToLower(model), "cloud") {
		return id, fmt.Errorf("облачные модели запрещены")
	}
	var tags struct {
		Models []struct {
			Name    string `json:"name"`
			Digest  string `json:"digest"`
			Size    int64  `json:"size"`
			Details struct {
				Quantization string `json:"quantization_level"`
			} `json:"details"`
		} `json:"models"`
	}
	if _, err := a.call(ctx, "/api/tags", nil, &tags); err != nil {
		return id, err
	}
	for _, tag := range tags.Models {
		if tag.Name == model || tag.Name == model+":latest" {
			id.Digest = tag.Digest
			id.Size = tag.Size
			id.Quantization = tag.Details.Quantization
		}
	}
	if id.Digest == "" {
		return id, fmt.Errorf("модель %s не установлена; выполните ollama pull %s", model, model)
	}
	var meta struct {
		RemoteHost   string   `json:"remote_host"`
		RemoteModel  string   `json:"remote_model"`
		Capabilities []string `json:"capabilities"`
	}
	if _, err := a.call(ctx, "/api/show", map[string]string{"model": model}, &meta); err != nil {
		return id, err
	}
	if meta.RemoteHost != "" || meta.RemoteModel != "" {
		return id, fmt.Errorf("удалённые модели запрещены")
	}
	for _, c := range meta.Capabilities {
		if c == capability {
			return id, nil
		}
	}
	return id, fmt.Errorf("у модели нет запрошенной возможности")
}
