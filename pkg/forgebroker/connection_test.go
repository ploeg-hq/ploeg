package forgebroker

import (
	"context"
	"errors"
	"testing"
)

type countingBroker struct {
	name    string
	mints   int
	revoked []string
}

func (b *countingBroker) Mint(_ context.Context, req MintRequest) (Credential, error) {
	b.mints++
	return Credential{ID: b.name + "-" + req.RunToken, Token: "tok-" + b.name}, nil
}

func (b *countingBroker) Revoke(_ context.Context, cred Credential) error {
	b.revoked = append(b.revoked, cred.ID)
	return nil
}

func TestAMintForAConnectionAsksThatConnectionAloneAndRevokesThroughIt(t *testing.T) {
	base := &countingBroker{name: "static"}
	conns := map[string]*countingBroker{"gh-acme": {name: "acme"}, "gh-other": {name: "other"}}
	var asked []string
	w := &WithConnections{Base: base, Resolve: func(_ context.Context, id string) (Broker, error) {
		asked = append(asked, id)
		b, ok := conns[id]
		if !ok {
			return nil, ErrConnectionCannotMint
		}
		return b, nil
	}}
	ctx := context.Background()
	cred, err := w.Mint(ctx, MintRequest{RunToken: "r1", Owner: "acme", Repo: "app", Connection: "gh-acme"})
	if err != nil || cred.Token != "tok-acme" || cred.ID != "connection:gh-acme|acme-r1" {
		t.Fatalf("mint = %+v, %v", cred, err)
	}
	if base.mints != 0 || conns["gh-other"].mints != 0 || len(asked) != 1 || asked[0] != "gh-acme" {
		t.Fatalf("base minted %d, other %d, asked %v; a runtime target's mint goes to its own connection only", base.mints, conns["gh-other"].mints, asked)
	}
	if err := w.Revoke(ctx, cred); err != nil || len(conns["gh-acme"].revoked) != 1 || conns["gh-acme"].revoked[0] != "acme-r1" || len(base.revoked) != 0 {
		t.Fatalf("revoke went to %v / %v, %v", conns["gh-acme"].revoked, base.revoked, err)
	}
	if _, err := w.Mint(ctx, MintRequest{RunToken: "r2", Owner: "acme", Repo: "app", Connection: "fj-team"}); !errors.Is(err, ErrConnectionCannotMint) || base.mints != 0 {
		t.Fatalf("a connection that cannot mint = %v, base mints %d; it must never fall back to the static broker", err, base.mints)
	}
	if cred, err := w.Mint(ctx, MintRequest{RunToken: "r3", Owner: "acme", Repo: "static"}); err != nil || cred.Token != "tok-static" {
		t.Fatalf("a static target's mint = %+v, %v", cred, err)
	}
	none := &WithConnections{}
	if cred, err := none.Mint(ctx, MintRequest{RunToken: "r4", Owner: "acme", Repo: "static"}); err != nil || cred.ID != "" {
		t.Fatalf("no base broker mints nothing for a static target: %+v, %v", cred, err)
	}
}
