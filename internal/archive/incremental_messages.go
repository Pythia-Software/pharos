package archive

import "database/sql"

type messageWriteState struct {
	id, hash string
	message  MessageRecord
}
type messageWriter struct {
	statement    *sql.Stmt
	conversation string
	states       map[string]messageWriteState
}

const messageWriteSQL = `INSERT INTO messages(id,conversation_id,native_id,role,kind,model,text,raw_text,source_order,created_at,parent_native_id,previous_native_id,call_id,evidence_locator,content_hash,selected,sender)
	VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(conversation_id,native_id) DO UPDATE SET
	role=excluded.role,kind=excluded.kind,model=excluded.model,text=excluded.text,raw_text=excluded.raw_text,source_order=excluded.source_order,
	created_at=excluded.created_at,parent_native_id=excluded.parent_native_id,previous_native_id=excluded.previous_native_id,call_id=excluded.call_id,
	evidence_locator=excluded.evidence_locator,content_hash=excluded.content_hash,selected=excluded.selected,sender=excluded.sender
	WHERE (messages.role,messages.kind,messages.model,messages.text,messages.raw_text,messages.source_order,messages.created_at,
		messages.parent_native_id,messages.previous_native_id,messages.call_id,messages.evidence_locator,messages.content_hash,messages.selected,messages.sender)
	IS NOT (excluded.role,excluded.kind,excluded.model,excluded.text,excluded.raw_text,excluded.source_order,excluded.created_at,
		excluded.parent_native_id,excluded.previous_native_id,excluded.call_id,excluded.evidence_locator,excluded.content_hash,excluded.selected,excluded.sender)`

func newMessageWriter(tx *sql.Tx, conversation string) (*messageWriter, error) {
	writer := &messageWriter{conversation: conversation, states: map[string]messageWriteState{}}
	rows, err := tx.Query(`SELECT native_id,id,content_hash,role,kind,COALESCE(model,''),text,COALESCE(raw_text,''),COALESCE(source_order,0),
		COALESCE(created_at,''),COALESCE(parent_native_id,''),COALESCE(previous_native_id,''),COALESCE(call_id,''),
		COALESCE(evidence_locator,''),selected,COALESCE(sender,'') FROM messages WHERE conversation_id=?`, conversation)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var native string
		var state messageWriteState
		var selected int
		message := &state.message
		if err := rows.Scan(&native, &state.id, &state.hash, &message.Role, &message.Kind, &message.Model, &message.Text, &message.RawText,
			&message.SourceOrder, &message.CreatedAt, &message.ParentNativeID, &message.PreviousNativeID, &message.CallID, &message.EvidenceLocator, &selected, &message.Sender); err != nil {
			rows.Close()
			return nil, err
		}
		message.NativeID, message.Selected = native, selected != 0
		writer.states[native] = state
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	writer.statement, err = tx.Prepare(messageWriteSQL)
	return writer, err
}

func (writer *messageWriter) write(message MessageRecord) (string, bool, error) {
	message.accounting = nil
	state, exists := writer.states[message.NativeID]
	id := state.id
	if !exists {
		id = stableID("message", writer.conversation, message.NativeID)
	}
	message.Role, message.Kind = defaultString(message.Role, "unknown"), defaultString(message.Kind, "message")
	hash := hashBytes([]byte(message.Text))
	if exists && state.message == message && state.hash == hash {
		return id, false, nil
	}
	_, err := writer.statement.Exec(id, writer.conversation, message.NativeID, message.Role, message.Kind, nilIfEmpty(message.Model), message.Text,
		nilIfEmpty(message.RawText), message.SourceOrder, nilIfEmpty(message.CreatedAt), nilIfEmpty(message.ParentNativeID), nilIfEmpty(message.PreviousNativeID),
		nilIfEmpty(message.CallID), nilIfEmpty(message.EvidenceLocator), hash, boolInt(message.Selected), nilIfEmpty(message.Sender))
	if err != nil {
		return "", false, err
	}
	writer.states[message.NativeID] = messageWriteState{id: id, hash: hash, message: message}
	return id, !exists || state.hash != hash || state.message.Kind != message.Kind, nil
}
