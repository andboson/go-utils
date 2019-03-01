package amqp_kit

import (
	log "github.com/sirupsen/logrus"
	"github.com/streadway/amqp"
	"sync"
)

type connectionCloseHandler interface {
	onCloseWithErr(conn *connection, err error)
}

type connection struct {
	config       *Config
	amqpConn     *amqp.Connection
	pool         *pool
	poolLock     sync.RWMutex
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

func (c *connection) getChan() (*channel, error) {
	var (
		channel *channel
		err     error
	)
	for i := 0; i < 5; i++ {
		c.poolLock.RLock()
		channel, err = c.pool.get()
		c.poolLock.RUnlock()
		if err == nil {
			break
		} else {
			log.Warnf("AMQP: got a channel with err %s", err.Error())
		}
	}
	return channel, err
}

func (c *connection) putChan(channel *channel) {
	c.poolLock.RLock()
	p := c.pool
	c.poolLock.RUnlock()
	if p != nil {
		p.put(channel)
	} else {
		channel.close()
	}
}

func (c *connection) emptyPool() {
	c.poolLock.Lock()
	defer c.poolLock.Unlock()

	c.pool.empty()
}

func (c *connection) close() error {
	c.poolLock.Lock()
	defer c.poolLock.Unlock()

	c.pool.empty()
	return c.amqpConn.Close()
}
