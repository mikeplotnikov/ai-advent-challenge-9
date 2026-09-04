package main

import (
	"fmt"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
)

// The ladder. The task asks for a weak, a medium and a strong model, and the
// host was asked point blank what makes one stronger than another: "это
// совокупность критериев, речь идет про количество параметров, квантизация,
// размер контекстного окна и так далее" [чат #1832], adding that it "очень
// сильно с ценой за токен коррелирует" [чат #1834].
//
// So every rung carries those criteria, and where a criterion is unavailable it
// says so instead of guessing. DeepSeek publishes neither a parameter count nor a
// quantization for its models; what it publishes is a context window and a price.
// Calling flash "medium" and pro "strong" therefore rests on price and on what
// this day measures — not on a parameter count nobody has.
//
// Three rungs are the assignment. The extra local sizes cost nothing to add and
// answer the question the assignment does not ask: where does quality stop
// improving as the model grows? That curve is what turns the day's conclusion
// into the routing the host himself proposed — a cheap model decides what kind of
// task this is, and the right configuration runs [чат #1796].
type rung struct {
	// id is what travels in the request.
	id string
	// tier is our label, and it is a claim: the reason lives in why.
	tier string
	why  string
	// params, quant and context are the host's criteria. "—" means the provider
	// does not publish it.
	params  string
	quant   string
	context string
	local   bool
	// memory is what `ollama ps` reported resident right after a live call on the
	// owner's machine. Measured, not derived from the file size — and it is the
	// "ресурсоёмкость" the task asks for, which a cloud model has no answer to.
	memory string
}

// ladder is ordered weakest-first as we label it, and the labels are what the
// day's measurements either support or contradict.
var ladder = []rung{
	{
		id: "qwen3:1.7b", tier: "слабая", local: true,
		params: "1.7B", quant: "q4_K_M", context: "40K", memory: "1.9 GB",
		why: "самая маленькая ступень, которая ещё отвечает по делу; 1.4 ГБ на диске, $0",
	},
	{
		id: "qwen3:4b", tier: "+ступень", local: true,
		params: "4B", quant: "q4_K_M", context: "256K", memory: "3.2 GB",
		why: "точка кривой «сколько даёт размер», в задании не требуется",
	},
	{
		id: "qwen3:8b", tier: "+ступень", local: true,
		params: "8B", quant: "q4_K_M", context: "40K", memory: "5.9 GB",
		why: "точка кривой «сколько даёт размер», в задании не требуется",
	},
	{
		id: "qwen3:14b", tier: "+ступень", local: true,
		params: "14B", quant: "q4_K_M", context: "40K", memory: "9.6 GB",
		why: "крупнейшая, влезающая в 11.8 GiB Metal на этой машине",
	},
	{
		id: "deepseek-v4-flash", tier: "средняя",
		params: "—", quant: "—", context: "1M", memory: "—",
		why: "облако, cache-miss $0.22 / выход $0.66 за 1M off-peak",
	},
	{
		id: "deepseek-v4-pro", tier: "сильная",
		params: "—", quant: "—", context: "1M", memory: "—",
		why: "облако, цена ×3 к flash при том же контексте",
	},
}

// clientFor builds the client for one rung. This is the switcher the host
// described — "можно сделать свитчер моделей у себя и по апи их вращать"
// [чат #1885]: one code path, and the model is a parameter of it. Everything that
// differs between providers is confined to this function.
func clientFor(r rung) (*llm.Client, error) {
	if r.local {
		return llm.NewLocal(r.id), nil
	}
	// Day 5 reads its own key so its spend can be capped and revoked on its own,
	// falling back to the shared one.
	c, err := llm.NewWithKeyEnv("DEEPSEEK_API_KEY_DAY5")
	if err != nil {
		return nil, err
	}
	// The environment may name a default model; the ladder overrides it, or the
	// grid would silently measure the same model six times.
	c.Model = r.id
	return c, nil
}

// registerFreePrices tells the pricing table which rungs cost nothing. What sits
// on this machine is not a property of the provider's API, so the day that runs
// the ladder owns the list.
func registerFreePrices() {
	for _, r := range ladder {
		if r.local {
			llm.FreeModel(r.id)
		}
	}
}

func (r rung) String() string {
	return fmt.Sprintf("%s (%s)", r.id, r.tier)
}
