package litellm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestModelPricesReadsEveryDeploymentOfTheGroup(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/model/info" || r.Header.Get("Authorization") != "Bearer master" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"data":[
			{"model_name":"free-qwen","model_info":{"input_cost_per_token":0,"output_cost_per_token":0}},
			{"model_name":"free-qwen","model_info":{"input_cost_per_token":0}},
			{"model_name":"paid","model_info":{"input_cost_per_token":0.000001,"output_cost_per_token":0.000002}}]}`)
	}))
	defer srv.Close()
	prices, err := NewClient(srv.URL+"/", "master").ModelPrices(context.Background(), "free-qwen")
	if err != nil {
		t.Fatal(err)
	}
	if len(prices) != 2 || !prices[0].Free() || prices[1].Free() {
		t.Fatalf("prices = %+v; want one free deployment and one without an output cost", prices)
	}
	if none, err := NewClient(srv.URL, "master").ModelPrices(context.Background(), "absent"); err != nil || len(none) != 0 {
		t.Fatalf("absent model = %+v, %v", none, err)
	}
	if _, err := NewClient(srv.URL, "wrong").ModelPrices(context.Background(), "free-qwen"); err == nil {
		t.Fatal("a refused read returned prices")
	}
}
