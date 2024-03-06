package maildav

import (
	"context"
	"io"
	"io/ioutil"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-message"
	"github.com/tarent/logrus"
	"github.com/targodan/go-errors"
)

var DefaultConnectionPool *ConnectionPool

func init() {
	DefaultConnectionPool = NewConnectionPool()
}

type Poller struct {
	config *PollerConfig
	cp     *ConnectionPool
}

type DestinationInfo struct {
	Config    *DestinationConfig
	Directory string
}

type Attachment struct {
	Filename        string
	Content         []byte
	DestinationInfo *DestinationInfo
}

func NewPoller(config *PollerConfig) (*Poller, error) {
	return &Poller{
		config: config,
		cp:     DefaultConnectionPool,
	}, nil
}

func (p *Poller) StartPolling(ctx context.Context, uploader *Uploader) error {
	run := true
	for run {
		attachments, err := p.Poll()
		if err != nil {
			logrus.WithField("source", p.config.SourceName).WithError(err).Error("Error while polling.")
		}

		err = uploader.UploadAttachments(attachments)
		if err != nil {
			logrus.WithField("source", p.config.SourceName).WithError(err).Error("Error while uploading.")
		}

		logrus.WithField("source", p.config.SourceName).WithError(ctx.Err()).Infof("Going to sleep for %v.", p.config.Timeout)
		select {
		case <-ctx.Done():
			logrus.WithField("source", p.config.SourceName).WithError(ctx.Err()).Info("Context canceled.")
			run = false
		case <-time.After(p.config.Timeout):
			logrus.WithField("source", p.config.SourceName).WithError(ctx.Err()).Info("Woke up.")
		}
	}
	return nil
}

func (p *Poller) Poll() ([]*Attachment, error) {
	attachments := []*Attachment{}

	c, err := p.cp.ConnectAndLock(p.config.SourceConfig)
	if err != nil {
		return attachments, err
	}
	defer c.Unlock()

	logrus.WithField("source", p.config.SourceName).Info("Scanning directories...")
	attachments, err = p.scanDirs(c)
	if err != nil {
		return attachments, errors.Wrap("error scanning directories", err)
	}
	logrus.WithField("source", p.config.SourceName).Info("Scanning successfull.")

	return attachments, nil
}

func (p *Poller) scanDirs(c IMAPClient) ([]*Attachment, error) {
	attachments := []*Attachment{}

	var errs error
	for _, dir := range p.config.SourceDirectories {
		logrus.WithField("source", p.config.SourceName).Info("Scanning directory \"" + dir + "\"...")
		attach, err := p.scanDir(c, dir)
		if err != nil {
			errs = errors.NewMultiError(errs, err)
		}
		attachments = append(attachments, attach...)
		logrus.WithField("source", p.config.SourceName).Info("Directory \"" + dir + "\" done.")
	}
	return attachments, errs
}

func (p *Poller) scanDir(c IMAPClient, dir string) ([]*Attachment, error) {
	attachments := []*Attachment{}

	_, err := c.Select(dir, false)
	if err != nil {
		logrus.WithError(err).Errorf("Could not open directory \"%s\".", dir)
		return attachments, err
	}

	unreadCrit := imap.NewSearchCriteria()
	unreadCrit.WithoutFlags = []string{imap.SeenFlag}
	ids, err := c.Search(unreadCrit)
	if err != nil {
		logrus.WithError(err).Errorf("Could not retreive unread messages in directory \"%s\".", dir)
		return attachments, err
	}
	if len(ids) == 0 {
		logrus.WithField("source", p.config.DestinationConfig.Name).Infof("No unread messages in directory \"%s\".", dir)
		return attachments, nil
	}

	seqset := new(imap.SeqSet)
	seqset.AddNum(ids...)
	section := &imap.BodySectionName{}
	items := []imap.FetchItem{
		section.FetchItem(),
	}

	messages := make(chan *imap.Message, 10)
	done := make(chan error, 1)
	go func() {
		done <- c.Fetch(seqset, items, messages)
		close(done)
	}()

	var errs error
	for msg := range messages {
		attach, err := p.parseMessage(msg, section)
		if err != nil {
			logrus.WithError(err).Error("Could not parse message.")
			errs = errors.NewMultiError(errs, err)
		}
		attachments = append(attachments, attach...)
		// TODO: Add support to optionally expunge mail after upload
	}

	if errs = errors.NewMultiError(errs, <-done); errs != nil {
		logrus.WithError(err).Error("Error fetching mail.")
		return attachments, err
	}
	return attachments, nil
}

func (p *Poller) parseMessage(raw *imap.Message, section *imap.BodySectionName) ([]*Attachment, error) {
	body := raw.GetBody(section)
	msg, err := message.Read(body)
	if err != nil {
		if message.IsUnknownCharset(err) {
			logrus.Warnf("Unknown encoding of message \"%du\".", raw.Uid)
		} else {
			return nil, errors.Wrap("could not read message", err)
		}
	}

	address := msg.Header.Get("From")
	foundValidSourceAddress := len(p.config.SourceAddresses) == 0
	for _, sourceAddress := range p.config.SourceAddresses {
		logrus.Infof("Checking if \"%s\" is a valid source address.", address)
		if sourceAddress == address {
			foundValidSourceAddress = true
			break
		}
	}

	if !foundValidSourceAddress {
		return nil, errors.Wrap("message from invalid source address", errors.New("message from invalid source address"))
	} else {
		logrus.Infof("Message from valid source address \"%s\".", address)
	}

	attachments := []*Attachment{}
	if mr := msg.MultipartReader(); mr != nil {
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			} else if err != nil {
				return nil, errors.Wrap("error reading next part of multipart message", err)
			}

			attach, err := p.parseMsgPart(part)
			if err != nil {
				return nil, errors.Wrap("error parsing part of multipart message", err)
			}
			if attach != nil {
				attachments = append(attachments, attach)
			}
		}
	} else {
		logrus.Warnf("Message \"%du\" is not multipart.", raw.Uid)
	}

	return attachments, nil
}

func (p *Poller) parseMsgPart(part *message.Entity) (*Attachment, error) {
	disp, params, err := part.Header.ContentDisposition()
	if err != nil {
		// Has no content disposition
		// TODO: Maybe add support for parsing email content and adding it as a text file
		// This is not an error, just skip it
		logrus.WithField("source", p.config.DestinationConfig.Name).Debug("Message part is has no Content-Disposition header.")
		return nil, nil
	}
	if disp == "attachment" {
		// TODO: is this robust? Do attachments always have the content disposition set?
		filename, ok := params["filename"]
		if !ok {
			// TODO: Can also be in "Content-Type" header under parameter "name"
			// TODO: Use a fallback random name or something
			//      (maybe, this would require reconstructing the file extension from mime type)
			return nil, errors.New("unable to handle attachment without filename in header")
		}
		encoding := part.Header.Get("Content-Transfer-Encoding")
		if encoding == "" {
			return nil, errors.New("unable to handle attachment without \"Content-Transfer-Encoding\" header")
		}

		// Note go-message already does base64 decoding where necessary
		content, err := ioutil.ReadAll(part.Body)
		if err != nil {
			return nil, errors.Wrap("error reading body of message part", err)
		}

		attachment := &Attachment{
			Filename: filename,
			Content:  content,
			DestinationInfo: &DestinationInfo{
				Config:    p.config.DestinationConfig,
				Directory: p.config.DestinationDirectory,
			},
		}
		logrus.WithField("source", p.config.DestinationConfig.Name).Infof("Successully downloaded file \"%s\".", filename)

		return attachment, nil
	}
	return nil, nil
}
