package archive

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"os"
	"testing"

	qt "github.com/Pythia-Software/query-table/backends/go"
)

const teamsExampleQuery = "/analyze?analyze_mode=query&analyze_topic=tokens&analyze_data=usage&q_usage=2d.qlZKV7KKrlbKTFGyUirJL0nMiS_Jz07NK1bSUcovULJSKi7NVdJRSi_KLy1wqlSyio7VUUrLTM3BUF4bWwsYAA"

func teamsTestQuery(token string) string {
	v := url.Values{"analyze_mode": {"query"}, "analyze_topic": {"tokens"}, "analyze_data": {"usage"}, "q_usage": {token}}
	return "/analyze?" + v.Encode()
}
func teamsRawToken(raw string) string {
	return "2j." + base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func TestTeamsCompileQuery(t *testing.T) {
	fixture,err:=os.ReadFile("../../testdata/teams/typed-usage-query-v1.json");if err!=nil {t.Fatal(err)}
	var vector struct{QueryURL string `json:"query_url"`};if json.Unmarshal(fixture,&vector)!=nil {t.Fatal("invalid Teams query vector")}
	if parsed,e:=compileTeamsQuery(vector.QueryURL);e!=nil || len(parsed.Metrics)!=1 || parsed.Metrics[0]!=(teamsMetric{ID:"total_tokens",Field:"total_tokens"}) {t.Fatalf("shared query vector: %+v %v",parsed,e)}
	p, err := compileTeamsQuery(teamsExampleQuery)
	if err != nil || len(p.Metrics) != 1 || p.Metrics[0] != (teamsMetric{ID: "total_tokens", Field: "total_tokens"}) {
		t.Fatalf("example: %+v %v", p, err)
	}
	q := qt.LinkQuery{Aggregations: []json.RawMessage{json.RawMessage(`{"id":"output","op":"sum","field":"output_tokens","groupBy":[]}`), json.RawMessage(`{"id":"all","op":"sum","field":"total_tokens","groupBy":[]}`)}}
	token, err := qt.EncodeQueryLink(q)
	if err != nil {
		t.Fatal(err)
	}
	p, err = compileTeamsQuery(teamsTestQuery(token))
	if err != nil || len(p.Metrics) != 2 {
		t.Fatalf("multiple numeric metrics: %+v %v", p, err)
	}
	for _, raw := range []string{
		`{"g":[{"id":"n","op":"sum","field":"total_tokens","groupBy":[]}],"unknown":true}`,
		`{"g":[{"id":"n","op":"sum","field":"total_tokens","groupBy":[],"where":[{"field":"title","op":"contains","value":"secret"}]}]}`,
		`{"g":[{"id":"n","op":"sum","field":"total_tokens","groupBy":[],"field":"output_tokens"}]}`,
		`{"g":[{"id":"n","op":"sum","field":"input_tokens","groupBy":[]}]}`,
		`{"g":[{"id":"n","op":"sum","field":"total_tokens","groupBy":["project"]}]}`,
		`{"g":[{"id":"n","op":"count","groupBy":[]}]}`,
		`{"g":[{"id":"n","op":"sum","field":"total_tokens","groupBy":[]}],"w":[{"field":"title","op":"contains","value":"secret"}]}`,
	} {
		if _, err = compileTeamsQuery(teamsTestQuery(teamsRawToken(raw))); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	for _, raw := range []string{"https://example.com" + teamsExampleQuery, teamsExampleQuery + "&reveal=foo", teamsExampleQuery + "&q_usage=duplicate", "/analyze?analyze_mode=query&analyze_topic=tokens&analyze_data=usage&q_usage=@saved"} {
		if _, err = compileTeamsQuery(raw); err == nil {
			t.Errorf("accepted URL %s", raw)
		}
	}
}
