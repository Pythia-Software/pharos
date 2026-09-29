package archive

import (
	"context"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Scripts and agents run coding agents headlessly (claude -p, agy -p, codex
// exec), and each run is stored as a conversation of its own that records
// nothing about what started it. A headless run is linked to the conversation
// whose shell command launched it, when exactly one conversation's command
// fits, on the same Mac:
//
//   - a command that runs that agent headlessly and was still running when the
//     run started; or
//   - for a script, which does not name the agent in its command: a script's
//     runs arrive as a stream (the same working directory, each within
//     launchStreamGap of the last), and the stream's first run starts moments
//     after the command that started the script, whether that command waits
//     for it or puts it in the background (&). The command must have started
//     within launchLead before the stream's first run and still be running
//     then, or have put work in the background. A run inside another work's
//     folder counts only for a command in that work or one naming its folder,
//     since apps (Conductor among them) start headless runs of their own there.
//     When commands in several conversations fit, the one conversation whose
//     command names the agent (python3 probe.py --cli agy) is taken.
//
// No command that began after a run started launched it.
//
// Links are derived with message_authorship and stored in
// conversation_launches.
const (
	// launchSlack allows for run start times an agent records to the second
	// (Antigravity), which may fall before the command that launched it.
	launchSlack = time.Second
	// launchLead bounds how long before a stream's first run the command that
	// started it began. Longer lets in commands other sessions happened to
	// be running.
	launchLead = 30 * time.Second
	// launchStreamGap ends a stream of runs.
	launchStreamGap = 10 * time.Minute
)

// temporaryFolders are where scripts often run headless agents; they belong to
// no work.
var temporaryFolders = []string{"/tmp", "/private/tmp", "/var/folders", "/private/var/folders"}

// latestLaunch is the latest a command that launched a run started at at may
// have begun.
func latestLaunch(at time.Time) time.Time {
	if at.Nanosecond() == 0 {
		return at.Add(launchSlack)
	}
	return at
}

// namesAgent reports whether a shell command has one of agents as a word.
func namesAgent(command string, agents map[string]bool) bool {
	for _, word := range shellWords(command) {
		if agents[strings.ToLower(path.Base(word))] {
			return true
		}
	}
	return false
}

// headlessLabels names each agent's headless invocation.
var headlessLabels = map[string]string{"claude": "claude -p", "agy": "agy -p", "codex": "codex exec"}

// headlessProgram names the agent ("claude", "agy", "codex") one parsed shell
// command runs headlessly, or returns "". program and subcommand are what
// shellProgram found in command.
func headlessProgram(program, subcommand, command string) string {
	switch program {
	case "codex":
		if subcommand == "exec" || subcommand == "e" {
			return program
		}
	case "claude", "agy":
		words := shellWords(command)
		for index, word := range words {
			if strings.ToLower(path.Base(word)) != program {
				continue
			}
			for _, flag := range words[index+1:] {
				if flag == "-p" || flag == "--print" || program == "agy" && flag == "--prompt" {
					return program
				}
			}
			return ""
		}
	}
	return ""
}

// headlessRun names the agent a top-level conversation's harness runs
// headlessly, or returns "". Claude Code records the entrypoint it was started
// through, and a claude -p started under another Claude session inherits that
// session's sdk-ts; its interactive terminal is "cli".
func headlessRun(provider, harness string) string {
	switch {
	case provider == "claude" && strings.HasPrefix(harness, "claude-code/sdk-"):
		return "claude"
	case provider == "antigravity" && harness == "antigravity-cli":
		return "agy"
	case provider == "codex" && harness == "codex/codex_exec":
		return "codex"
	}
	return ""
}

// conversationLaunch links a headless run to the command that launched it.
type conversationLaunch struct {
	child, parent, toolCall, message, program string
}

// launchConversation is what linking needs to know about a conversation.
type launchConversation struct {
	id, workspace, location, parent, host, provider, harness string
	started                                                  time.Time
}

// launchCommand is a shell command that may have launched a run.
type launchCommand struct {
	conversation, toolCall, message, text string
	started, ended                        time.Time
	background                            bool
}

// launchIndex answers which work a conversation's commands count toward.
type launchIndex struct {
	conversations  map[string]*launchConversation
	representative map[string]string
	counterpart    map[string]string
	runs           map[string]bool
	// works holds the folders of works a person drove.
	works map[string]bool
}

// root is the top-level conversation a sub-agent chain started in.
func (index *launchIndex) root(id string) string {
	for range 32 {
		conversation := index.conversations[id]
		if conversation == nil || conversation.parent == "" {
			break
		}
		id = conversation.parent
	}
	return id
}

// family names the work a conversation's commands count toward: its root
// agent's, through mirrors.
func (index *launchIndex) family(id string) string {
	if conversation := index.conversations[index.root(id)]; conversation != nil {
		return defaultString(index.representative[conversation.workspace], conversation.workspace)
	}
	return id
}

// eligible reports whether command, from a conversation other than the
// run's own on the same Mac, may have launched run.
func (index *launchIndex) eligible(run *launchConversation, command launchCommand) bool {
	parent := index.conversations[command.conversation]
	if parent == nil || index.runs[index.root(command.conversation)] || index.family(command.conversation) == index.family(run.id) {
		return false
	}
	return run.host == "" || parent.host == "" || run.host == parent.host
}

// owner is the folder of the innermost work a person drove that holds
// folder, or "" for a temporary folder or one no such work holds.
func (index *launchIndex) owner(folder string) string {
	if folder == "" || scratchFolder(folder) {
		return ""
	}
	for current := folder; ; current = filepath.Dir(current) {
		if index.works[current] {
			return current
		}
		if parent := filepath.Dir(current); parent == current {
			return ""
		}
	}
}

// inWork reports whether command's conversation, or its root's or mirror's,
// works in a folder holding folder.
func (index *launchIndex) inWork(command launchCommand, folder string) bool {
	for _, id := range []string{command.conversation, index.root(command.conversation), index.counterpart[index.root(command.conversation)]} {
		if conversation := index.conversations[id]; conversation != nil && conversation.location != "" && within(folder, conversation.location) {
			return true
		}
	}
	return false
}

// only returns the command of the one family that ran any of commands,
// preferring the command recorded by the work standing for its mirrors, then
// the latest to start.
func (index *launchIndex) only(commands []launchCommand) (launchCommand, bool) {
	var chosen launchCommand
	family := ""
	for _, command := range commands {
		key := index.family(command.conversation)
		if family != "" && key != family {
			return launchCommand{}, false
		}
		mirrored := index.representative[index.conversations[command.conversation].workspace] != ""
		if family == "" || index.representative[index.conversations[chosen.conversation].workspace] != "" || !mirrored {
			chosen = command
		}
		family = key
	}
	return chosen, family != ""
}

// link names the conversation standing for command's: when only the mirror
// recorded the command, its counterpart, whose messages have other IDs.
func (index *launchIndex) link(run *launchConversation, command launchCommand, program string) conversationLaunch {
	parent, message := command.conversation, command.message
	if other := index.conversations[index.counterpart[parent]]; index.representative[index.conversations[parent].workspace] != "" && other != nil && index.representative[other.workspace] == "" {
		parent, message = other.id, ""
	}
	return conversationLaunch{child: run.id, parent: parent, toolCall: command.toolCall, message: message, program: program}
}

// scratchFolder reports whether folder is a temporary folder, on any Mac.
func scratchFolder(folder string) bool {
	for _, root := range temporaryFolders {
		if within(folder, root) {
			return true
		}
	}
	return false
}

// deriveLaunches links each headless run no app started (claimed holds the
// conversations an app such as Conductor records starting) to the command
// that launched it. representative maps a mirrored work to the work that
// stands for it, whose conversation the link names when it has the command.
func (c *Catalog) deriveLaunches(ctx context.Context, claimed map[string]bool, representative map[string]string) ([]conversationLaunch, error) {
	index := &launchIndex{conversations: map[string]*launchConversation{}, representative: representative, counterpart: map[string]string{}, runs: map[string]bool{}, works: map[string]bool{}}
	rows, err := c.DB.QueryContext(ctx, `SELECT c.id,c.workspace_id,COALESCE(w.location,''),COALESCE(c.parent_id,''),COALESCE(c.origin_host_id,''),c.provider,COALESCE(c.harness,''),COALESCE(c.started_at,'')
		FROM conversations c JOIN workspaces w ON w.id=c.workspace_id`)
	if err != nil {
		return nil, err
	}
	runs := []*launchConversation{}
	for rows.Next() {
		conversation := &launchConversation{}
		var started string
		if err := rows.Scan(&conversation.id, &conversation.workspace, &conversation.location, &conversation.parent, &conversation.host, &conversation.provider, &conversation.harness, &started); err != nil {
			rows.Close()
			return nil, err
		}
		if conversation.location != "" {
			conversation.location = filepath.Clean(conversation.location)
		}
		conversation.started, _ = parseTime(started)
		index.conversations[conversation.id] = conversation
		if conversation.parent == "" && !conversation.started.IsZero() && !claimed[conversation.id] && headlessRun(conversation.provider, conversation.harness) != "" {
			runs = append(runs, conversation)
			index.runs[conversation.id] = true
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(runs) == 0 {
		return nil, nil
	}
	for _, conversation := range index.conversations {
		if location := conversation.location; location != "" && location != "/" && !scratchFolder(location) && !index.runs[index.root(conversation.id)] {
			index.works[location] = true
		}
	}
	links, err := c.DB.QueryContext(ctx, `SELECT left_id,right_id FROM conversation_identity_links`)
	if err != nil {
		return nil, err
	}
	for links.Next() {
		var left, right string
		if err := links.Scan(&left, &right); err != nil {
			links.Close()
			return nil, err
		}
		index.counterpart[left], index.counterpart[right] = right, left
	}
	links.Close()
	if err := links.Err(); err != nil {
		return nil, err
	}
	named, err := c.headlessCommands(ctx)
	if err != nil {
		return nil, err
	}
	// Runs in start order, split into streams by Mac and folder.
	sort.Slice(runs, func(i, j int) bool { return runs[i].started.Before(runs[j].started) })
	type streamKey struct{ host, location string }
	streams, last := map[streamKey][][]*launchConversation{}, map[streamKey]time.Time{}
	keys := []streamKey{}
	for _, run := range runs {
		key := streamKey{run.host, run.location}
		if _, seen := last[key]; !seen {
			keys = append(keys, key)
		}
		if previous, seen := last[key]; !seen || run.started.Sub(previous) > launchStreamGap {
			streams[key] = append(streams[key], nil)
		}
		streams[key][len(streams[key])-1] = append(streams[key][len(streams[key])-1], run)
		last[key] = run.started
	}
	launches := []conversationLaunch{}
	for _, key := range keys {
		for _, stream := range streams[key] {
			first := stream[0]
			candidates, err := c.launchCandidates(ctx, first.started)
			if err != nil {
				return nil, err
			}
			fitting := []launchCommand{}
			owner := index.owner(first.location)
			for _, command := range candidates {
				if !index.eligible(first, command) || command.ended.Before(first.started) && !command.background {
					continue
				}
				if owner != "" && !index.inWork(command, first.location) && !strings.Contains(command.text, owner) {
					continue
				}
				fitting = append(fitting, command)
			}
			starter, started := index.only(fitting)
			if !started && len(fitting) > 1 {
				agents, naming := map[string]bool{}, []launchCommand{}
				for _, run := range stream {
					agents[headlessRun(run.provider, run.harness)] = true
				}
				for _, command := range fitting {
					if namesAgent(command.text, agents) {
						naming = append(naming, command)
					}
				}
				starter, started = index.only(naming)
			}
			for _, run := range stream {
				program := headlessRun(run.provider, run.harness)
				// A command naming the agent that was running then is the
				// surest evidence.
				naming := []launchCommand{}
				for _, command := range named[program] {
					if command.started.After(latestLaunch(run.started)) {
						break
					}
					if !command.ended.Before(run.started) && index.eligible(run, command) {
						naming = append(naming, command)
					}
				}
				if command, ok := index.only(naming); ok {
					launches = append(launches, index.link(run, command, program))
				} else if started {
					launches = append(launches, index.link(run, starter, program))
				}
			}
		}
	}
	return launches, nil
}

// headlessCommands returns, by agent in start order, the shell commands that
// ran an agent headlessly.
func (c *Catalog) headlessCommands(ctx context.Context) (map[string][]launchCommand, error) {
	rows, err := c.DB.QueryContext(ctx, `SELECT t.tool_call_id,t.program,COALESCE(t.subcommand,''),t.command,c.conversation_id,COALESCE(c.call_message_id,''),c.started_at,c.ended_at
		FROM tool_commands t JOIN tool_calls c ON c.id=t.tool_call_id
		WHERE t.program IN ('claude','agy','codex') AND c.started_at IS NOT NULL AND c.ended_at IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	commands := map[string][]launchCommand{}
	for rows.Next() {
		var command launchCommand
		var program, subcommand, started, ended string
		if err := rows.Scan(&command.toolCall, &program, &subcommand, &command.text, &command.conversation, &command.message, &started, &ended); err != nil {
			return nil, err
		}
		program = headlessProgram(program, subcommand, command.text)
		var ok bool
		if command.started, ok = parseTime(started); !ok || program == "" {
			continue
		}
		if command.ended, ok = parseTime(ended); ok {
			commands[program] = append(commands[program], command)
		}
	}
	for _, list := range commands {
		sort.Slice(list, func(i, j int) bool { return list[i].started.Before(list[j].started) })
	}
	return commands, rows.Err()
}

// launchCandidates returns the shell commands that started within launchLead
// before at, which the first run of a stream started at.
func (c *Catalog) launchCandidates(ctx context.Context, at time.Time) ([]launchCommand, error) {
	rows, err := c.DB.QueryContext(ctx, `SELECT id,conversation_id,COALESCE(call_message_id,''),COALESCE(command,''),started_at,ended_at,backgrounded
		FROM tool_calls WHERE tool_category='command' AND started_at BETWEEN ? AND ? AND ended_at IS NOT NULL`,
		formatTime(at.Add(-launchLead)), formatTime(latestLaunch(at)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	commands := []launchCommand{}
	for rows.Next() {
		var command launchCommand
		var started, ended string
		if err := rows.Scan(&command.toolCall, &command.conversation, &command.message, &command.text, &started, &ended, &command.background); err != nil {
			return nil, err
		}
		var ok bool
		if command.started, ok = parseTime(started); !ok {
			continue
		}
		if command.ended, ok = parseTime(ended); ok {
			commands = append(commands, command)
		}
	}
	return commands, rows.Err()
}
