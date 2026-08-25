package mcpserver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// *** CALL IT THE WAY A CLIENT WITH NO SCHEMA CALLS IT. ***
//
// ken-prod-ops ran the include_instructions test from inside the exact population the argument
// exists for — a session whose captured ken_version schema has no such property — and it failed
// where no test here could see:
//
//	on 3.22.0: -> validating "arguments": unexpected additional properties ["include_instructions"]
//	on 3.23.0: -> validating /properties/include_instructions: type: true has type "string", want "boolean"
//
// The error CHANGING is the good news: the property existed and the argument crossed the freeze,
// which is the premise the whole feature rests on. Then their client, having no schema to tell it
// the value was a boolean, serialized it as the string "true", and schema validation refused the
// call before any handler ran.
//
// **A client with no schema for a property cannot type that property correctly.** So the callers
// this feature is FOR are precisely the ones a boolean-only contract excludes.
//
// THE REASON NO EXISTING TEST CAUGHT IT is prod's, and it generalises: every test written inside
// this repository has the schema available, so every test types the argument correctly and passes.
// The thing under test behaves differently for the caller who actually has the problem — the same
// shape as asserting on a const while the delivered string is truncated.
//
// So this test sends the RAW JSON a schema-less client sends, through the real transport, and
// asserts the instructions come back.
func TestIncludeInstructionsAcceptsAStringFromASchemalessClient(t *testing.T) {
	t.Setenv("KEN_DEV_TOKEN", "dev-secret")
	srv, _ := testServer(t)
	ctx := context.Background()

	cli := mcp.NewClient(&mcp.Implementation{Name: "schemaless", Version: "0"}, nil)
	sess, err := cli.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: srv.URL, HTTPClient: clientWithToken("dev-secret"), DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	for _, raw := range []string{`"true"`, `true`, `"yes"`, `"1"`} {
		var args map[string]any
		if err := json.Unmarshal([]byte(`{"include_instructions":`+raw+`}`), &args); err != nil {
			t.Fatal(err)
		}
		res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "ken_version", Arguments: args})
		if err != nil {
			t.Errorf("include_instructions=%s was refused before the handler ran: %v\n"+
				"This is the shape a client without the schema sends, and it is the only shape that "+
				"population can send.", raw, err)
			continue
		}
		if res.IsError {
			t.Errorf("include_instructions=%s returned an error result: %+v", raw, res.Content)
			continue
		}
		var out struct {
			Instructions *struct {
				Instructions string `json:"instructions"`
				Surface      string `json:"surface"`
			} `json:"instructions"`
		}
		decodeResult(t, res, &out)
		if out.Instructions == nil || out.Instructions.Instructions == "" {
			t.Errorf("include_instructions=%s was accepted and returned no instructions — worse than a "+
				"refusal, because the session cannot tell it was ignored", raw)
		}
	}

	// AND THE DEFAULT STAYS CHEAP. ken_version is the call a session makes to notice it is stale;
	// attaching kilobytes to every one of them is how sessions stop making it.
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "ken_version"})
	if err != nil {
		t.Fatal(err)
	}
	var plain map[string]any
	decodeResult(t, res, &plain)
	if _, present := plain["instructions"]; present {
		t.Error("a bare ken_version carries the instructions; the argument exists so that the common call " +
			"does not pay for the rare one")
	}
}
