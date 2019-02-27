package amqp_kit

import (
	log "github.com/sirupsen/logrus"
	"github.com/streadway/amqp"
)

type connectionCloseHandler interface {
	onCloseWithErr(conn *connection, err error)
}

type connection struct {
	config       *Config
	amqpConn     *amqp.Connection
	closeHandler connectionCloseHandler
}

func newConnection(config *Config, closeHandler connectionCloseHandler) *connection {
	conn := &connection{
		config:       config,
		closeHandler: closeHandler,
	}
	return conn
}

func (c *connection) connect() error {
	amqpConn, err := amqp.Dial(MakeDsn(c.config))
	if err != nil {
		return err
	}
	c.amqpConn = amqpConn

	notifyChan := make(chan *amqp.Error)
	amqpConn.NotifyClose(notifyChan)
	go func() {
		e := <-notifyChan
		if e != nil {
			c.closeHandler.onCloseWithErr(c, e)
		}
	}()

	return nil
}

func (c *connection) getChan() (*amqp.Channel, error) {
	var (
		channel *amqp.Channel
		err     error
	)
	for i := 0; i < 5; i++ {
		channel, err = c.amqpConn.Channel()
		if err == nil {
			break
		} else {
			log.Warnf("AMQP: got a channel with err %s", err.Error())
		}
	}
	return channel, err
}
