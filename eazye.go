package eazye

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"io"
	"net/mail"
	"time"

	"github.com/mxk/go-imap/imap"
	_ "github.com/paulrosania/go-charset/data"
	"golang.org/x/net/html"
)

// MailboxInfo holds onto the credentials and other information.
// needed for connecting to an IMAP server.
type Client struct {
	TLS    bool
	Folder string
	// Read only mode, false (original logic) if not initialized
	ReadOnly bool

	Imap *imap.Client
}

// Option is a type which represents a functional option.
type Option func(*Client)

// SetFolder is a functional option to set the Folder attr.
func SetFolder(folder string) Option {
	return func(c *Client) {
		c.Folder = folder
	}
}

// SetFolder is a functional option to set the ReadOnly attr.
func SetReadOnly(readOnly bool) Option {
	return func(c *Client) {
		c.ReadOnly = readOnly
	}
}

// SetTLS is a functional option to set the TLS attr.
func SetTLS(tls bool) Option {
	return func(c *Client) {
		c.TLS = tls
	}
}

// New initializes  a new Client.
func New(host, user, pwd string, options ...func(*Client)) (*Client, error) {
	client := &Client{
		TLS:      false,
		ReadOnly: false,
	}

	for _, option := range options {
		option(client)
	}

	var imapClient *imap.Client
	var err error
	if client.TLS {
		imapClient, err = imap.DialTLS(host, new(tls.Config))
		if err != nil {
			return client, err
		}
	} else {
		imapClient, err = imap.Dial(host)
		if err != nil {
			return client, err
		}
	}

	_, err = imapClient.Login(user, pwd)
	if err != nil {
		return client, err
	}

	_, err = imap.Wait(imapClient.Select(client.Folder, client.ReadOnly))
	if err != nil {
		return client, err
	}

	client.Imap = imapClient

	return client, nil
}

// GetAll will pull all emails from the email folder and return them as a list.
func (c *Client) GetAll(markAsRead, delete bool) ([]Email, error) {
	// call chan, put 'em in a list, return
	var emails []Email
	responses, err := c.GenerateAll(markAsRead, delete)
	if err != nil {
		return emails, err
	}

	for resp := range responses {
		if resp.Err != nil {
			return emails, resp.Err
		}
		emails = append(emails, resp.Email)
	}

	return emails, nil
}

// GenerateAll will find all emails in the email folder and pass them along to the responses channel.
func (c *Client) GenerateAll(markAsRead, delete bool) (chan Response, error) {
	return c.generateMail("ALL", nil, markAsRead, delete)
}

// GetUnread will find all unread emails in the folder and return them as a list.
func (c *Client) GetUnread(markAsRead, delete bool) ([]Email, error) {
	// call chan, put 'em in a list, return
	var emails []Email

	responses, err := c.GenerateUnread(markAsRead, delete)
	if err != nil {
		return emails, err
	}

	for resp := range responses {
		if resp.Err != nil {
			return emails, resp.Err
		}
		emails = append(emails, resp.Email)
	}

	return emails, nil
}

// GenerateUnread will find all unread emails in the folder and pass them along to the responses channel.
func (c *Client) GenerateUnread(markAsRead, delete bool) (chan Response, error) {
	return c.generateMail("UNSEEN", nil, markAsRead, delete)
}

// GetSince will pull all emails that have an internal date after the given time.
func (c *Client) GetSince(since time.Time, markAsRead, delete bool) ([]Email, error) {
	var emails []Email
	responses, err := c.GenerateSince(since, markAsRead, delete)
	if err != nil {
		return emails, err
	}

	for resp := range responses {
		if resp.Err != nil {
			return emails, resp.Err
		}
		emails = append(emails, resp.Email)
	}

	return emails, nil
}

// GenerateSince will find all emails that have an internal date after the given time and pass them along to the
// responses channel.
func (c *Client) GenerateSince(since time.Time, markAsRead, delete bool) (chan Response, error) {
	return c.generateMail("", &since, markAsRead, delete)
}

// Email is a raw Email message from the std lib
type Email struct {
	ID      imap.Field
	Message *mail.Message
}

var (
	styleTag       = []byte("style")
	scriptTag      = []byte("script")
	headTag        = []byte("head")
	metaTag        = []byte("meta")
	doctypeTag     = []byte("doctype")
	shapeTag       = []byte("v:shape")
	imageDataTag   = []byte("v:imagedata")
	commentTag     = []byte("!")
	nonVisibleTags = [][]byte{
		styleTag,
		scriptTag,
		headTag,
		metaTag,
		doctypeTag,
		shapeTag,
		imageDataTag,
		commentTag,
	}
)

func VisibleText(body io.Reader) ([][]byte, error) {
	var (
		text [][]byte
		skip bool
		err  error
	)
	z := html.NewTokenizer(body)
	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			if err = z.Err(); err == io.EOF {
				return text, nil
			}
			return text, err
		case html.TextToken:
			if !skip {
				tmp := bytes.TrimSpace(z.Text())
				if len(tmp) == 0 {
					continue
				}
				tagText := make([]byte, len(tmp))
				copy(tagText, tmp)
				text = append(text, tagText)
			}
		case html.StartTagToken, html.EndTagToken:
			tn, _ := z.TagName()
			for _, nvTag := range nonVisibleTags {
				if bytes.Equal(tn, nvTag) {
					skip = (tt == html.StartTagToken)
					break
				}
			}
		}
	}
	return text, nil
}

// Response is a helper struct to wrap the email responses and possible errors.
type Response struct {
	Email Email
	Err   error
}

const dateFormat = "02-Jan-2006"

// findEmails will run a find the UIDs of any emails that match the search.:
func (c *Client) findEmails(search string, since *time.Time) (*imap.Command, error) {
	var specs []imap.Field
	if len(search) > 0 {
		specs = append(specs, search)
	}

	if since != nil {
		sinceStr := since.Format(dateFormat)
		specs = append(specs, "SINCE", sinceStr)
	}

	// get headers and UID for UnSeen message in src inbox...
	cmd, err := imap.Wait(c.Imap.UIDSearch(specs...))
	if err != nil {
		return &imap.Command{}, fmt.Errorf("uid search failed: %s", err)
	}
	return cmd, nil
}

var GenerateBufferSize = 100

func (c *Client) generateMail(search string, since *time.Time, markAsRead, delete bool) (chan Response, error) {
	var err error
	responses := make(chan Response, GenerateBufferSize)

	go func() {
		defer func() {
			// c.Imap.Close(true)
			// c.Imap.Logout(30 * time.Second)
			close(responses)
		}()

		var cmd *imap.Command
		// find all the UIDs
		cmd, err = c.findEmails(search, since)
		if err != nil {
			responses <- Response{Err: err}
			return
		}
		// gotta fetch 'em all
		c.getEmails(cmd, markAsRead, delete, responses)
	}()

	return responses, nil
}

func (c *Client) getEmails(cmd *imap.Command, markAsRead, delete bool, responses chan Response) {
	seq := &imap.SeqSet{}
	msgCount := 0
	for _, rsp := range cmd.Data {
		for _, uid := range rsp.SearchResults() {
			msgCount++
			seq.AddNum(uid)
		}
	}

	// nothing to request?! why you even callin me, foolio?
	if seq.Empty() {
		return
	}

	fCmd, err := imap.Wait(c.Imap.UIDFetch(seq, "INTERNALDATE", "BODY[]", "UID", "RFC822.HEADER"))
	if err != nil {
		responses <- Response{Err: fmt.Errorf("unable to perform uid fetch: %s", err)}
		return
	}

	var email Email
	for _, msgData := range fCmd.Data {
		msgFields := msgData.MessageInfo().Attrs

		// make sure is a legit response before we attempt to parse it
		// deal with unsolicited FETCH responses containing only flags
		// I'm lookin' at YOU, Gmail!
		// http://mailman13.u.washington.edu/pipermail/imap-protocol/2014-October/002355.html
		// http://stackoverflow.com/questions/26262472/gmail-imap-is-sometimes-returning-bad-results-for-fetch
		if _, ok := msgFields["RFC822.HEADER"]; !ok {
			continue
		}

		email, err = newEmail(msgFields)
		if err != nil {
			responses <- Response{Err: fmt.Errorf("unable to parse email: %s", err)}
			return
		}

		responses <- Response{Email: email}

		if !markAsRead {
			err = c.SetAsUnread(email)
			if err != nil {
				responses <- Response{Err: fmt.Errorf("unable to remove seen flag: %s", err)}
				return
			}
		}

		if delete {
			err = c.DeleteEmail(email)
			if err != nil {
				responses <- Response{Err: fmt.Errorf("unable to delete email: %s", err)}
				return
			}
		}
	}
	return
}

func (c *Client) DeleteEmail(email Email) error {
	return c.alterEmail(email, "\\DELETED", true)
}

func (c *Client) SetAsUnread(email Email) error {
	return c.alterEmail(email, "\\SEEN", false)
}

func (c *Client) SetAsRead(email Email) error {
	return c.alterEmail(email, "\\SEEN", true)
}

func (c *Client) alterEmail(email Email, flag string, plus bool) error {
	UID := imap.AsNumber(email.ID)
	flg := "-FLAGS"
	if plus {
		flg = "+FLAGS"
	}
	fSeq := &imap.SeqSet{}
	fSeq.AddNum(UID)
	_, err := imap.Wait(c.Imap.UIDStore(fSeq, flg, flag))
	if err != nil {
		return err
	}

	return nil
}

// newEmailMessage will parse an imap.FieldMap into an Email. This
// will expect the message to container the internaldate and the body with
// all headers included.
func newEmail(msgFields imap.FieldMap) (Email, error) {
	// parse the header
	var message bytes.Buffer

	message.Write(imap.AsBytes(msgFields["RFC822.HEADER"]))
	message.Write([]byte("\n\n"))
	rawBody := imap.AsBytes(msgFields["BODY[]"])
	message.Write(rawBody)

	msg, err := mail.ReadMessage(&message)
	if err != nil {
		return Email{}, fmt.Errorf("unable to read header: %s", err)
	}

	email := Email{
		ID:      msgFields["UID"],
		Message: msg,
	}

	return email, nil
}
