package archive

import (
	"encoding/json"
	"strings"
)

// A reply keeps decoded values for provenance and the encoded byte ranges
// for classification, action evidence and labeling transforms.
type structuredQuestionAnswer struct {
	title, value            string
	titleRange, answerRange [2]int
}

type structuredQuestionReply struct {
	authorshipForm
	answers []structuredQuestionAnswer
}

// Structured question replies carry generated questions and IDs alongside the
// person's answers. Keep original byte offsets, including JSON escapes, just
// as page-feedback forms do. Only recognize complete, unfenced reply blocks.
func structuredQuestionReplies(text string, bodyStart int) []structuredQuestionReply {
	const open = "<send_user_message_question_reply>"
	const close = "</send_user_message_question_reply>"
	if !strings.Contains(text, open) {
		return nil
	}
	var replies []structuredQuestionReply
	fenced, consumed := false, 0
	for _, line := range textLines(text) {
		if line[0] < consumed || line[1] < bodyStart {
			continue
		}
		value := strings.TrimSpace(text[line[0]:line[1]])
		if strings.HasPrefix(value, "```") || strings.HasPrefix(value, "~~~") {
			fenced = !fenced
		}
		if fenced || value != open {
			continue
		}
		start := line[0] + strings.Index(text[line[0]:line[1]], open)
		if start < bodyStart {
			continue
		}
		body := line[1]
		d := json.NewDecoder(strings.NewReader(text[body:]))
		answers, ok := questionReplyAnswers(body, d)
		if !ok {
			continue
		}
		end := body + int(d.InputOffset())
		end += len(text[end:]) - len(strings.TrimLeft(text[end:], " \t\r\n"))
		if !strings.HasPrefix(text[end:], close) {
			continue
		}
		end += len(close)
		reply := structuredQuestionReply{authorshipForm: authorshipForm{start: line[0], end: end}, answers: answers}
		for _, answer := range answers {
			reply.notes = append(reply.notes, feedbackJSONNotes(text, answer.answerRange[0], answer.answerRange[1])...)
		}
		replies = append(replies, reply)
		consumed = end
	}
	return replies
}

// Decode fields rather than matching string literals: keys can be reordered,
// and nested metadata or escaped answer-like text must never become answers.
func questionReplyAnswers(offset int, d *json.Decoder) ([]structuredQuestionAnswer, bool) {
	if token, err := d.Token(); err != nil || token != json.Delim('[') {
		return nil, false
	}
	var answers []structuredQuestionAnswer
	for d.More() {
		if token, err := d.Token(); err != nil || token != json.Delim('{') {
			return nil, false
		}
		fields := map[string]json.RawMessage{}
		var answer structuredQuestionAnswer
		for d.More() {
			token, err := d.Token()
			key, ok := token.(string)
			if err != nil || !ok || fields[key] != nil {
				return nil, false
			}
			var raw json.RawMessage
			if d.Decode(&raw) != nil {
				return nil, false
			}
			fields[key] = raw
			if key == "answer" || key == "question" {
				end := offset + int(d.InputOffset())
				bounds := [2]int{end - len(raw) + 1, end - 1}
				if key == "answer" {
					answer.answerRange = bounds
				} else {
					answer.titleRange = bounds
				}
			}
		}
		if _, err := d.Token(); err != nil {
			return nil, false
		}
		for _, key := range []string{"questionItemId", "question", "answer"} {
			var value string
			if len(fields[key]) == 0 || fields[key][0] != '"' || json.Unmarshal(fields[key], &value) != nil || key != "answer" && value == "" {
				return nil, false
			}
		}
		_ = json.Unmarshal(fields["question"], &answer.title)
		_ = json.Unmarshal(fields["answer"], &answer.value)
		answers = append(answers, answer)
	}
	if _, err := d.Token(); err != nil {
		return nil, false
	}
	return answers, true
}

// Apply the same recorded-question provenance as flattened replies. A matching
// selected option is a submitted action, never freshly typed answer text.
func (c *classifier) claimStructuredQuestionAnswers(l *labeler, input authorInput, replies []structuredQuestionReply) {
	for _, reply := range replies {
		for _, answer := range reply.answers {
			questions := c.questions[input.ConversationID]
			for i := len(questions) - 1; i >= 0; i-- {
				q := questions[i]
				if q.title != answer.title {
					continue
				}
				option, generated := questionOption(q.options, answer.value)
				if !generated {
					continue
				}
				l.claim(answer.answerRange[0], answer.answerRange[1], spanHarness, reasonQuestionForm, q.origin)
				if option != "" {
					c.answers = append(c.answers, questionAnswer{Title: q.title, Option: option, TitleStart: answer.titleRange[0], TitleEnd: answer.titleRange[1],
						Start: answer.answerRange[0], End: answer.answerRange[1], Origin: q.origin})
				}
				break
			}
		}
	}
}
