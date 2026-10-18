package litellm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// ModelPrice is what the gateway charges per token for one deployment of a
// model group. A nil cost is one the gateway does not state.
type ModelPrice struct {
	Model      string
	InputCost  *float64
	OutputCost *float64
}

// Free reports whether the deployment states both costs and both are zero.
func (p ModelPrice) Free() bool {
	return p.InputCost != nil && p.OutputCost != nil && *p.InputCost == 0 && *p.OutputCost == 0
}

// BaseURL is the proxy the client talks to.
func (c *Client) BaseURL() string { return strings.TrimRight(c.baseURL, "/") }

// ModelPrices reads every deployment of the model group named model from
// GET /model/info. An empty result means the gateway serves no such group.
func (c *Client) ModelPrices(ctx context.Context, model string) ([]ModelPrice, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL()+"/model/info", nil)
	if err != nil {
		return nil, fmt.Errorf("litellm: invalid model info endpoint")
	}
	req.Header.Set("Authorization", "Bearer "+c.masterKey)
	resp, err := c.httpCli.Do(req)
	if err != nil {
		return nil, fmt.Errorf("litellm: model info request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("litellm: model info got HTTP %d", resp.StatusCode)
	}
	var info struct {
		Data []struct {
			ModelName string `json:"model_name"`
			ModelInfo struct {
				InputCost  *float64 `json:"input_cost_per_token"`
				OutputCost *float64 `json:"output_cost_per_token"`
			} `json:"model_info"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&info); err != nil {
		return nil, fmt.Errorf("litellm: invalid model info response")
	}
	var out []ModelPrice
	for _, d := range info.Data {
		if d.ModelName == model {
			out = append(out, ModelPrice{Model: d.ModelName, InputCost: d.ModelInfo.InputCost, OutputCost: d.ModelInfo.OutputCost})
		}
	}
	return out, nil
}
