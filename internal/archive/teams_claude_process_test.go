package archive

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTeamsClaudeProcessChecksBeforePromptAndAfterResult(t *testing.T) {
	dir := t.TempDir()
	controlRaw, err := os.ReadFile("../../testdata/teams/claude-control-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	authRaw, err := os.ReadFile("../../testdata/teams/claude-auth-status-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var events []json.RawMessage
	if json.Unmarshal(controlRaw, &events) != nil || len(events) != 3 {
		t.Fatal("fixture")
	}
	account, ok := teamsClaudeInitialize(events[0], "init")
	if !ok {
		t.Fatal("account fixture")
	}
	auth, ok := teamsClaudeAuthDigest(authRaw)
	if !ok {
		t.Fatal("auth fixture")
	}
	initEvent := strings.Replace(syntheticClaudeInit, "synthetic-model", "claude-opus-5-5", 1)
	for name, data := range map[string][]byte{"controls.json": controlRaw, "auth.json": authRaw, "init.json": []byte(initEvent)} {
		if err = os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	script := `#!/usr/bin/python3
import os,sys,json,time
p=os.path.dirname(__file__)
def load(name):
 with open(os.path.join(p,name)) as f: return json.load(f)
def send(v):
 print(json.dumps(v,separators=(',',':')),flush=True)
if os.environ.get('PHAROS_TEST_PRIVATE_CANARY'): sys.exit(20)
if 'auth' in sys.argv: send(load('auth.json')); sys.exit(0)
controls=load('controls.json')
for line in sys.stdin:
 msg=json.loads(line)
 if msg.get('type')=='control_request':
  sub=msg['request']['subtype']; i={'initialize':0,'get_settings':1,'get_hooks_listing':2}[sub]
  response=controls[i]; response['response']['request_id']=msg['request_id']
  if sub=='initialize': response['response']['response']['pid']=os.getpid()
  if sub=='get_settings' and os.path.exists(os.path.join(p,'deny')): response['response']['response']['effective']['env']={'ANTHROPIC_BASE_URL':'https://synthetic.invalid'}
  send(response)
  if sub=='get_hooks_listing' and os.path.exists(os.path.join(p,'stall')): time.sleep(60)
 elif msg.get('type')=='user':
  open(os.path.join(p,'prompt-received'),'w').close()
  send({'type':'system','subtype':'ui_invalidate','event':'ui.render','uuid':'synthetic-id','session_id':'synthetic-session'})
  send(load('init.json'))
  send({'type':'system','subtype':'thinking_tokens','estimated_tokens':50,'estimated_tokens_delta':50,'uuid':'synthetic-id','session_id':'synthetic-session'})
  send({'type':'assistant','message':{'role':'assistant','content':[{'type':'tool_use','name':'StructuredOutput','id':'tool-1','input':{'answer':'synthetic'}}]}})
  send({'type':'user','message':{'role':'user','content':[{'type':'tool_result','tool_use_id':'tool-1','content':'ok'}]}})
  send({'type':'result','subtype':'success','is_error':False,'permission_denials':[],'modelUsage':{'claude-opus-5-5':{'provider':'firstParty','webSearchRequests':0}},'usage':{'server_tool_use':{'web_search_requests':0,'web_fetch_requests':0}},'subagent_stats':{'spawned':0},'structured_output':{'answer':'synthetic'}})
`
	path := filepath.Join(dir, "synthetic-claude")
	if err = os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	override(t, &teamsClaudeReviewedExecutable, func(p string) (string, error) { return p, nil })
	t.Setenv("PHAROS_TEST_PRIVATE_CANARY", "must-not-inherit")
	provider := askProvider{ID: "Claude", path: path, Models: []askModel{{ID: "claude-opus-5-5", Efforts: []string{"Low"}}}}
	selection := askSelection{Provider: "Claude", Model: "claude-opus-5-5", Effort: "Low"}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	open := func() *teamsClaudeStream {
		s, e := teamsOpenClaude(ctx, provider, selection, []byte(`{"type":"object","properties":{"answer":{"type":"string","maxLength":64}},"required":["answer"],"additionalProperties":false}`), teamsClaudeReviewedAccount{Session: account, Auth: auth})
		if e != nil {
			t.Fatal(e)
		}
		return s
	}
	s := open()
	s.turnMu.Lock()
	if _, err = s.turn(ctx, "concurrent"); err == nil {
		s.turnMu.Unlock()
		s.stop()
		t.Fatal("concurrent turn accepted")
	}
	s.turnMu.Unlock()
	result, err := s.turn(ctx, "Synthetic authorized prompt")
	if err != nil || string(result) != `{"answer":"synthetic"}` {
		s.stop()
		t.Fatalf("turn %s: %v", result, err)
	}
	result, err = s.turn(ctx, "Second synthetic prompt")
	if err != nil || string(result) != `{"answer":"synthetic"}` {
		s.stop()
		t.Fatalf("second turn %s: %v", result, err)
	}
	s.stop()
	if _, err = s.turn(ctx, "closed"); err == nil {
		t.Fatal("closed process accepted turn")
	}
	if err = os.Remove(filepath.Join(dir, "prompt-received")); err != nil {
		t.Fatal(err)
	}
	s = open()
	defer s.stop()
	if err = os.WriteFile(filepath.Join(dir, "deny"), []byte("synthetic"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = s.turn(ctx, "Must not leave host"); err == nil {
		t.Fatal("changed routing accepted")
	}
	if _, err = os.Stat(filepath.Join(dir, "prompt-received")); !os.IsNotExist(err) {
		t.Fatal("prompt reached CLI after failed preflight")
	}
	s.stop()
	if err = os.Remove(filepath.Join(dir, "deny")); err != nil {
		t.Fatal(err)
	}
	s = open()
	if err = os.WriteFile(filepath.Join(dir, "stall"), []byte("synthetic"), 0600); err != nil {
		t.Fatal(err)
	}
	short, cancelShort := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancelShort()
	started := time.Now()
	if _, err = s.turn(short, strings.Repeat("x", 128<<10)); err == nil {
		t.Fatal("stalled process accepted")
	}
	if time.Since(started) > 3*time.Second {
		t.Fatal("deadline did not stop blocked stdin")
	}
}
