package api

import "context"

// MailServer is the current validated SMTP configuration. Password is opened
// only for delivery and excluded from JSON; callers must never log this value.
// It grants no authority to send to a recipient. TLS requests STARTTLS, while
// SSL requests an encrypted connection from the first byte.
type MailServer struct {
	Enabled                             bool
	Host                                string
	Port                                int
	Username                            string
	Password                            string `json:"-"`
	From, SubjectPrefix, ArtifactoryURL string
	TLS, SSL                            bool
}

// Mail reads the singleton mail server without opening a network connection.
// Missing configuration returns a wrapped kernel.ErrNotFound. Disabled settings
// are returned with Enabled false, so the delivery owner can suppress mail.
type Mail interface {
	Server(context.Context) (MailServer, error)
}
