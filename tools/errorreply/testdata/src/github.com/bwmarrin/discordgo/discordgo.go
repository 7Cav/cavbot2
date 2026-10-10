// Package discordgo stands in for github.com/bwmarrin/discordgo, with the
// shapes of the types the testdata sends and the errors it reads.
package discordgo

import (
	"net/http"
	"time"
)

type Interaction struct{}

type InteractionCreate struct {
	*Interaction
}

type MessageEmbedField struct {
	Name  string
	Value string
}

type MessageEmbed struct {
	Title  string
	Fields []*MessageEmbedField
}

type InteractionResponseData struct {
	Content string
	Embeds  []*MessageEmbed
}

type InteractionResponse struct {
	Type int
	Data *InteractionResponseData
}

type WebhookEdit struct {
	Content *string
	Embeds  *[]*MessageEmbed
}

type WebhookParams struct {
	Content string
}

type MessageSend struct {
	Content string
}

type MessageEdit struct {
	Content *string
	ID      string
	Channel string
}

type Message struct{}

type Channel struct{}

type ChannelEdit struct {
	Name string
}

type GuildChannelCreateData struct {
	Name string
}

type RequestOption func()

type Session struct{}

func (s *Session) ChannelMessageSend(channelID string, content string, options ...RequestOption) (*Message, error) {
	return nil, nil
}

func (s *Session) ChannelEdit(channelID string, data *ChannelEdit, options ...RequestOption) (*Channel, error) {
	return nil, nil
}

func (s *Session) GuildChannelCreateComplex(guildID string, data GuildChannelCreateData, options ...RequestOption) (*Channel, error) {
	return nil, nil
}

func (s *Session) ChannelMessageSendComplex(channelID string, data *MessageSend, options ...RequestOption) (*Message, error) {
	return nil, nil
}

func (s *Session) ChannelMessageEditComplex(m *MessageEdit, options ...RequestOption) (*Message, error) {
	return nil, nil
}

func (s *Session) InteractionRespond(interaction *Interaction, resp *InteractionResponse, options ...RequestOption) error {
	return nil
}

func (s *Session) InteractionResponseEdit(interaction *Interaction, newresp *WebhookEdit, options ...RequestOption) (*Message, error) {
	return nil, nil
}

func (s *Session) FollowupMessageCreate(interaction *Interaction, wait bool, data *WebhookParams, options ...RequestOption) (*Message, error) {
	return nil, nil
}

type APIErrorMessage struct {
	Code    int
	Message string
}

type RESTError struct {
	Request      *http.Request
	Response     *http.Response
	ResponseBody []byte

	Message *APIErrorMessage
}

func (r RESTError) Error() string {
	return "HTTP " + r.Response.Status + ", " + string(r.ResponseBody)
}

type TooManyRequests struct {
	Bucket     string
	Message    string
	RetryAfter time.Duration
}

type RateLimit struct {
	*TooManyRequests
	URL string
}

type RateLimitError struct {
	*RateLimit
}

func (e RateLimitError) Error() string {
	return "Rate limit exceeded on " + e.URL + ", retry after " + e.RetryAfter.String()
}
