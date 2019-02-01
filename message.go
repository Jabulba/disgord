package disgord

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/andersfylling/disgord/constant"
	"github.com/andersfylling/disgord/endpoint"
	"github.com/andersfylling/disgord/httd"
)

// different message acticity types
const (
	_ = iota
	MessageActivityTypeJoin
	MessageActivityTypeSpectate
	MessageActivityTypeListen
	MessageActivityTypeJoinRequest
)

// The different message types usually generated by Discord. eg. "a new user joined"
const (
	MessageTypeDefault = iota
	MessageTypeRecipientAdd
	MessageTypeRecipientRemove
	MessageTypeCall
	MessageTypeChannelNameChange
	MessageTypeChannelIconChange
	MessageTypeChannelPinnedMessage
	MessageTypeGuildMemberJoin
)

const (
	AttachmentSpoilerPrefix = "SPOILER_"
)

// NewMessage ...
func NewMessage() *Message {
	return &Message{}
}

//func NewDeletedMessage() *DeletedMessage {
//	return &DeletedMessage{}
//}

//type DeletedMessage struct {
//	ID        Snowflake `json:"id"`
//	ChannelID Snowflake `json:"channel_id"`
//}

// MessageActivity https://discordapp.com/developers/docs/resources/channel#message-object-message-activity-structure
type MessageActivity struct {
	Type    int    `json:"type"`
	PartyID string `json:"party_id"`
}

// MessageApplication https://discordapp.com/developers/docs/resources/channel#message-object-message-application-structure
type MessageApplication struct {
	ID          Snowflake `json:"id"`
	CoverImage  string    `json:"cover_image"`
	Description string    `json:"description"`
	Icon        string    `json:"icon"`
	Name        string    `json:"name"`
}

// Message https://discordapp.com/developers/docs/resources/channel#message-object-message-structure
type Message struct {
	Lockable        `json:"-"`
	ID              Snowflake          `json:"id"`
	ChannelID       Snowflake          `json:"channel_id"`
	Author          *User              `json:"author"`
	Content         string             `json:"content"`
	Timestamp       time.Time          `json:"timestamp"`
	EditedTimestamp time.Time          `json:"edited_timestamp"` // ?
	Tts             bool               `json:"tts"`
	MentionEveryone bool               `json:"mention_everyone"`
	Mentions        []*User            `json:"mentions"`
	MentionRoles    []Snowflake        `json:"mention_roles"`
	Attachments     []*Attachment      `json:"attachments"`
	Embeds          []*ChannelEmbed    `json:"embeds"`
	Reactions       []*Reaction        `json:"reactions"`       // ?
	Nonce           Snowflake          `json:"nonce,omitempty"` // ?, used for validating a message was sent
	Pinned          bool               `json:"pinned"`
	WebhookID       Snowflake          `json:"webhook_id"` // ?
	Type            uint               `json:"type"`
	Activity        MessageActivity    `json:"activity"`
	Application     MessageApplication `json:"application"`

	// SpoilerTagContent is only true if the entire message text is tagged as a spoiler (aka completely wrapped in ||)
	SpoilerTagContent        bool `json:"-"`
	SpoilerTagAllAttachments bool `json:"-"`
}

func (m *Message) updateInternals() {
	if len(m.Content) >= len("||||") {
		prefix := m.Content[0:2]
		suffix := m.Content[len(m.Content)-2 : len(m.Content)]
		m.SpoilerTagContent = prefix+suffix == "||||"
	}

	m.SpoilerTagAllAttachments = len(m.Attachments) > 0
	for i := range m.Attachments {
		m.Attachments[i].updateInternals()
		if !m.Attachments[i].SpoilerTag {
			m.SpoilerTagAllAttachments = false
			break
		}
	}
}

// TODO: why is this method needed?
//func (m *Message) MarshalJSON() ([]byte, error) {
//	if m.ID.Empty() {
//		return []byte("{}"), nil
//	}
//
//	//TODO: remove copying of mutex
//	return json.Marshal(Message(*m))
//}

// TODO: await for caching
//func (m *Message) DirectMessage(session Session) bool {
//	return m.Type == ChannelTypeDM
//}

type messageDeleter interface {
	DeleteMessage(channelID, msgID Snowflake) (err error)
}

// DeepCopy see interface at struct.go#DeepCopier
func (m *Message) DeepCopy() (copy interface{}) {
	copy = NewMessage()
	m.CopyOverTo(copy)

	return
}

// CopyOverTo see interface at struct.go#Copier
func (m *Message) CopyOverTo(other interface{}) (err error) {
	var message *Message
	var valid bool
	if message, valid = other.(*Message); !valid {
		err = newErrorUnsupportedType("argument given is not a *Message type")
		return
	}

	if constant.LockedMethods {
		m.RLock()
		message.Lock()
	}

	message.ID = m.ID
	message.ChannelID = m.ChannelID
	message.Content = m.Content
	message.Timestamp = m.Timestamp
	message.EditedTimestamp = m.EditedTimestamp
	message.Tts = m.Tts
	message.MentionEveryone = m.MentionEveryone
	message.MentionRoles = m.MentionRoles
	message.Pinned = m.Pinned
	message.WebhookID = m.WebhookID
	message.Type = m.Type
	message.Activity = m.Activity
	message.Application = m.Application

	if m.Author != nil {
		message.Author = m.Author.DeepCopy().(*User)
	}

	if !m.Nonce.Empty() {
		message.Nonce = m.Nonce
	}

	for _, mention := range m.Mentions {
		message.Mentions = append(message.Mentions, mention.DeepCopy().(*User))
	}

	for _, attachment := range m.Attachments {
		message.Attachments = append(message.Attachments, attachment.DeepCopy().(*Attachment))
	}

	for _, embed := range m.Embeds {
		message.Embeds = append(message.Embeds, embed.DeepCopy().(*ChannelEmbed))
	}

	for _, reaction := range m.Reactions {
		message.Reactions = append(message.Reactions, reaction.DeepCopy().(*Reaction))
	}

	if constant.LockedMethods {
		m.RUnlock()
		message.Unlock()
	}

	return
}

func (m *Message) deleteFromDiscord(session Session) (err error) {
	if m.ID.Empty() {
		err = newErrorMissingSnowflake("message is missing snowflake")
		return
	}

	err = session.DeleteMessage(m.ChannelID, m.ID)
	return
}
func (m *Message) saveToDiscord(session Session) (err error) {
	var message *Message
	if m.ID.Empty() {
		message, err = m.Send(session)
	} else {
		message, err = m.update(session)
	}

	message.CopyOverTo(m)
	return
}

// MessageUpdater is a interface which only holds the message update method
type MessageUpdater interface {
	UpdateMessage(message *Message) (msg *Message, err error)
}

// Update after changing the message object, call update to notify Discord about any changes made
func (m *Message) update(client MessageUpdater) (msg *Message, err error) {
	msg, err = client.UpdateMessage(m)
	return
}

// MessageSender is an interface which only holds the method needed for creating a channel message
type MessageSender interface {
	CreateChannelMessage(channelID Snowflake, params *CreateChannelMessageParams) (ret *Message, err error)
}

// Send sends this message to discord.
func (m *Message) Send(client MessageSender) (msg *Message, err error) {

	if constant.LockedMethods {
		m.RLock()
	}
	params := &CreateChannelMessageParams{
		Content: m.Content,
		Tts:     m.Tts,
		Nonce:   m.Nonce,
		// File: ...
		// Embed: ...
	}
	if len(m.Embeds) > 0 {
		params.Embed = m.Embeds[0]
	}
	channelID := m.ChannelID

	if constant.LockedMethods {
		m.RUnlock()
	}

	msg, err = client.CreateChannelMessage(channelID, params)
	return
}

// Respond responds to a message using a Message object.
func (m *Message) Respond(client MessageSender, message *Message) (msg *Message, err error) {
	if constant.LockedMethods {
		m.RLock()
	}
	id := m.ChannelID
	if constant.LockedMethods {
		m.RUnlock()
	}

	if constant.LockedMethods {
		message.Lock()
	}
	message.ChannelID = id
	if constant.LockedMethods {
		message.Unlock()
	}
	msg, err = message.Send(client)
	return
}

// RespondString sends a reply to a message in the form of a string
func (m *Message) RespondString(client MessageSender, content string) (msg *Message, err error) {
	params := &CreateChannelMessageParams{
		Content: content,
	}

	if constant.LockedMethods {
		m.RLock()
	}
	msg, err = client.CreateChannelMessage(m.ChannelID, params)
	if constant.LockedMethods {
		m.RUnlock()
	}
	return
}

// AddReaction adds a reaction to the message
//func (m *Message) AddReaction(reaction *Reaction) {}

// RemoveReaction removes a reaction from the message
//func (m *Message) RemoveReaction(id Snowflake)    {}

// GetChannelMessagesParams https://discordapp.com/developers/docs/resources/channel#get-channel-messages-query-string-params
// TODO: ensure limits
type GetChannelMessagesParams struct {
	Around Snowflake `urlparam:"around,omitempty"`
	Before Snowflake `urlparam:"before,omitempty"`
	After  Snowflake `urlparam:"after,omitempty"`
	Limit  int       `urlparam:"limit,omitempty"`
}

// GetQueryString .
func (params *GetChannelMessagesParams) GetQueryString() string {
	separator := "?"
	query := ""

	if !params.Around.Empty() {
		query += separator + params.Around.String()
		separator = "&"
	}

	if !params.Before.Empty() {
		query += separator + params.Before.String()
		separator = "&"
	}

	if !params.After.Empty() {
		query += separator + params.After.String()
		separator = "&"
	}

	if params.Limit > 0 {
		query += separator + strconv.Itoa(params.Limit)
	}

	return query
}

// GetChannelMessages [REST] Returns the messages for a channel. If operating on a guild channel, this endpoint requires
// the 'VIEW_CHANNEL' permission to be present on the current user. If the current user is missing
// the 'READ_MESSAGE_HISTORY' permission in the channel then this will return no messages
// (since they cannot read the message history). Returns an array of message objects on success.
//  Method                  GET
//  Endpoint                /channels/{channel.id}/messages
//  Rate limiter [MAJOR]    /channels/{channel.id}/messages
//  Discord documentation   https://discordapp.com/developers/docs/resources/channel#get-channel-messages
//  Reviewed                2018-06-10
//  Comment                 The before, after, and around keys are mutually exclusive, only one may
//                          be passed at a time. see ReqGetChannelMessagesParams.
func GetChannelMessages(client httd.Getter, channelID Snowflake, params URLParameters) (ret []*Message, err error) {
	if channelID.Empty() {
		err = errors.New("channelID must be set to get channel messages")
		return
	}
	query := ""
	if params != nil {
		query += params.GetQueryString()
	}

	_, body, err := client.Get(&httd.Request{
		Ratelimiter: ratelimitChannelMessages(channelID),
		Endpoint:    endpoint.ChannelMessages(channelID) + query,
	})
	if err != nil {
		return
	}

	ret = []*Message{}
	err = unmarshal(body, ret)
	for i := range ret {
		ret[i].updateInternals()
	}
	return
}

// GetChannelMessage [REST] Returns a specific message in the channel. If operating on a guild channel, this endpoints
// requires the 'READ_MESSAGE_HISTORY' permission to be present on the current user.
// Returns a message object on success.
//  Method                  GET
//  Endpoint                /channels/{channel.id}/messages/{message.id}
//  Rate limiter [MAJOR]    /channels/{channel.id}/messages
//  Discord documentation   https://discordapp.com/developers/docs/resources/channel#get-channel-message
//  Reviewed                2018-06-10
//  Comment                 -
func GetChannelMessage(client httd.Getter, channelID, messageID Snowflake) (ret *Message, err error) {
	if channelID.Empty() {
		err = errors.New("channelID must be set to get channel messages")
		return
	}
	if messageID.Empty() {
		err = errors.New("messageID must be set to get a specific message from a channel")
		return
	}

	_, body, err := client.Get(&httd.Request{
		Ratelimiter: ratelimitChannelMessages(channelID),
		Endpoint:    endpoint.ChannelMessage(channelID, messageID),
	})
	if err != nil {
		return
	}

	ret = &Message{}
	err = unmarshal(body, ret)
	ret.updateInternals()
	return
}

// NewMessageByString creates a message object from a string/content
func NewMessageByString(content string) *CreateChannelMessageParams {
	return &CreateChannelMessageParams{
		Content: content,
	}
}

// CreateChannelMessageParams JSON params for CreateChannelMessage
type CreateChannelMessageParams struct {
	Content string        `json:"content"`
	Nonce   Snowflake     `json:"nonce,omitempty"`
	Tts     bool          `json:"tts,omitempty"`
	Embed   *ChannelEmbed `json:"embed,omitempty"` // embedded rich content

	Files []CreateChannelMessageFileParams `json:"-"` // Always omit as this is included in multipart, not JSON payload

	SpoilerTagContent        bool `json:"-"`
	SpoilerTagAllAttachments bool `json:"-"`
}

func (p *CreateChannelMessageParams) prepare() (postBody interface{}, contentType string, err error) {
	// spoiler tag
	if p.SpoilerTagContent && len(p.Content) > 0 {
		p.Content = "|| " + p.Content + " ||"
	}
	if p.SpoilerTagAllAttachments {
		for i := range p.Files {
			p.Files[i].SpoilerTag = true
		}
	}
	for i := range p.Files {
		name := p.Files[i].FileName
		if p.Files[i].SpoilerTag && !strings.HasPrefix(name, "SPOILER_") {
			p.Files[i].FileName = "SPOILER_" + name
		}
	}

	if len(p.Files) == 0 {
		postBody = p
		contentType = httd.ContentTypeJSON
		return
	}

	// Set up a new multipart writer, as we'll be using this for the POST body instead
	buf := new(bytes.Buffer)
	mp := multipart.NewWriter(buf)

	// Write the existing JSON payload
	var payload []byte
	payload, err = json.Marshal(p)
	if err != nil {
		return
	}
	if err = mp.WriteField("payload_json", string(payload)); err != nil {
		return
	}

	// Iterate through all the files and write them to the multipart blob
	for i, file := range p.Files {
		if err = file.write(i, mp); err != nil {
			return
		}
	}

	mp.Close()

	postBody = buf
	contentType = mp.FormDataContentType()

	return
}

// CreateChannelMessageFileParams contains the information needed to upload a file to Discord, it is part of the
// CreateChannelMessageParams struct.
type CreateChannelMessageFileParams struct {
	Reader     io.Reader `json:"-"` // always omit as we don't want this as part of the JSON payload
	FileName   string    `json:"-"`
	SpoilerTag bool      `json:"-"`
}

// write helper for file uploading in messages
func (f *CreateChannelMessageFileParams) write(i int, mp *multipart.Writer) error {
	w, err := mp.CreateFormFile("file"+strconv.FormatInt(int64(i), 10), f.FileName)
	if err != nil {
		return err
	}

	if _, err = io.Copy(w, f.Reader); err != nil {
		return err
	}

	return nil
}

// CreateChannelMessage [REST] Post a message to a guild text or DM channel. If operating on a guild channel, this
// endpoint requires the 'SEND_MESSAGES' permission to be present on the current user. If the tts field is set to true,
// the SEND_TTS_MESSAGES permission is required for the message to be spoken. Returns a message object. Fires a
// Message Create Gateway event. See message formatting for more information on how to properly format messages.
// The maximum request size when sending a message is 8MB.
//  Method                  POST
//  Endpoint                /channels/{channel.id}/messages
//  Rate limiter [MAJOR]    /channels/{channel.id}/messages
//  Discord documentation   https://discordapp.com/developers/docs/resources/channel#create-message
//  Reviewed                2018-06-10
//  Comment                 Before using this endpoint, you must connect to and identify with a gateway at least once.
func CreateChannelMessage(client httd.Poster, channelID Snowflake, params *CreateChannelMessageParams) (ret *Message, err error) {
	if channelID.Empty() {
		err = errors.New("channelID must be set to get channel messages")
		return
	}
	if params == nil {
		err = errors.New("message must be set")
		return
	}

	var (
		postBody    interface{}
		contentType string
	)

	postBody, contentType, err = params.prepare()
	if err != nil {
		return
	}

	_, body, err := client.Post(&httd.Request{
		Ratelimiter: ratelimitChannelMessages(channelID),
		Endpoint:    "/channels/" + channelID.String() + "/messages",
		Body:        postBody,
		ContentType: contentType,
	})

	if err != nil {
		return
	}

	ret = &Message{}
	err = unmarshal(body, ret)
	ret.updateInternals()
	return
}

// EditMessageParams https://discordapp.com/developers/docs/resources/channel#edit-message-json-params
type EditMessageParams struct {
	Content string        `json:"content,omitempty"`
	Embed   *ChannelEmbed `json:"embed,omitempty"` // embedded rich content
}

// EditMessage [REST] Edit a previously sent message. You can only edit messages that have been sent by the
// current user. Returns a message object. Fires a Message Update Gateway event.
//  Method                  PATCH
//  Endpoint                /channels/{channel.id}/messages/{message.id}
//  Rate limiter [MAJOR]    /channels/{channel.id}/messages
//  Discord documentation   https://discordapp.com/developers/docs/resources/channel#edit-message
//  Reviewed                2018-06-10
//  Comment                 All parameters to this endpoint are optional.
func EditMessage(client httd.Patcher, chanID, msgID Snowflake, params *EditMessageParams) (ret *Message, err error) {
	if chanID.Empty() {
		err = errors.New("channelID must be set to get channel messages")
		return
	}
	if msgID.Empty() {
		err = errors.New("msgID must be set to edit the message")
		return
	}

	_, body, err := client.Patch(&httd.Request{
		Ratelimiter: ratelimitChannelMessages(chanID),
		Endpoint:    "/channels/" + chanID.String() + "/messages/" + msgID.String(),
		Body:        params,
		ContentType: httd.ContentTypeJSON,
	})
	if err != nil {
		return
	}

	ret = &Message{}
	err = unmarshal(body, ret)
	ret.updateInternals()
	return
}

// DeleteMessage [REST] Delete a message. If operating on a guild channel and trying to delete a message that was not
// sent by the current user, this endpoint requires the 'MANAGE_MESSAGES' permission. Returns a 204 empty response
// on success. Fires a Message Delete Gateway event.
//  Method                  DELETE
//  Endpoint                /channels/{channel.id}/messages/{message.id}
//  Rate limiter [MAJOR]    /channels/{channel.id}/messages [DELETE]
//  Discord documentation   https://discordapp.com/developers/docs/resources/channel#delete-message
//  Reviewed                2018-06-10
//  Comment                 -
func DeleteMessage(client httd.Deleter, channelID, msgID Snowflake) (err error) {
	if channelID.Empty() {
		err = errors.New("channelID must be set to get channel messages")
		return
	}
	if msgID.Empty() {
		err = errors.New("msgID must be set to delete the message")
		return
	}

	resp, _, err := client.Delete(&httd.Request{
		Ratelimiter: ratelimitChannelMessagesDelete(channelID),
		Endpoint:    endpoint.ChannelMessage(channelID, msgID),
	})
	if err != nil {
		return
	}

	if resp.StatusCode != http.StatusNoContent {
		msg := "unexpected http response code. Got " + resp.Status + ", wants " + http.StatusText(http.StatusNoContent)
		err = errors.New(msg)
	}
	return
}

// BulkDeleteMessagesParams https://discordapp.com/developers/docs/resources/channel#bulk-delete-messages-json-params
type BulkDeleteMessagesParams struct {
	Messages []Snowflake `json:"messages"`
	m        sync.RWMutex
}

func (p *BulkDeleteMessagesParams) tooMany(messages int) (err error) {
	if messages > 100 {
		err = errors.New("must be 100 or less messages to delete")
	}

	return
}

func (p *BulkDeleteMessagesParams) tooFew(messages int) (err error) {
	if messages < 2 {
		err = errors.New("must be at least two messages to delete")
	}

	return
}

// Valid validates the BulkDeleteMessagesParams data
func (p *BulkDeleteMessagesParams) Valid() (err error) {
	p.m.RLock()
	defer p.m.RUnlock()

	messages := len(p.Messages)
	err = p.tooMany(messages)
	if err != nil {
		return
	}
	err = p.tooFew(messages)
	return
}

// AddMessage Adds a message to be deleted
func (p *BulkDeleteMessagesParams) AddMessage(msg *Message) (err error) {
	p.m.Lock()
	defer p.m.Unlock()

	err = p.tooMany(len(p.Messages) + 1)
	if err != nil {
		return
	}

	// TODO: check for duplicates as those are counted only once

	p.Messages = append(p.Messages, msg.ID)
	return
}

// BulkDeleteMessages [REST] Delete multiple messages in a single request. This endpoint can only be used on guild
// channels and requires the 'MANAGE_MESSAGES' permission. Returns a 204 empty response on success. Fires multiple
// Message Delete Gateway events.Any message IDs given that do not exist or are invalid will count towards
// the minimum and maximum message count (currently 2 and 100 respectively). Additionally, duplicated IDs
// will only be counted once.
//  Method                  POST
//  Endpoint                /channels/{channel.id}/messages/bulk-delete
//  Rate limiter [MAJOR]    /channels/{channel.id}/messages [DELETE] TODO: is this limiter key incorrect?
//  Discord documentation   https://discordapp.com/developers/docs/resources/channel#delete-message
//  Reviewed                2018-06-10
//  Comment                 This endpoint will not delete messages older than 2 weeks, and will fail if any message
//                          provided is older than that.
func BulkDeleteMessages(client httd.Poster, chanID Snowflake, params *BulkDeleteMessagesParams) (err error) {
	if chanID.Empty() {
		err = errors.New("channelID must be set to get channel messages")
		return
	}
	err = params.Valid()
	if err != nil {
		return
	}

	resp, _, err := client.Post(&httd.Request{
		Ratelimiter: ratelimitChannelMessagesDelete(chanID),
		Endpoint:    endpoint.ChannelMessagesBulkDelete(chanID),
		ContentType: httd.ContentTypeJSON,
	})
	if err != nil {
		return
	}

	if resp.StatusCode != http.StatusNoContent {
		msg := "unexpected http response code. Got " + resp.Status + ", wants " + http.StatusText(http.StatusNoContent)
		err = errors.New(msg)
	}
	return
}
