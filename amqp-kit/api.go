package amqp_kit

import (
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-kit/kit/endpoint"
	log "github.com/sirupsen/logrus"
	"github.com/streadway/amqp"
)

type Server struct {
	conn           *connection
	connLock       sync.RWMutex
	subs           []SubscribeInfo
	config         *Config
	stopClientChan chan struct{}
}

type SubscribeInfo struct {
	Name        string
	Queue       string
	Workers     int
	PubExchange string
	E           endpoint.Endpoint
	Dec         DecodeRequestFunc
	Enc         EncodeResponseFunc
	O           []SubscriberOption
}

type Config struct {
	Address         string
	User            string
	Password        string
	VirtualHost     string
	ChannelPoolSize int
}

func New(s []SubscribeInfo, cfg *Config) (*Server, error) {
	ser := &Server{
		subs:           s,
		config:         cfg,
		stopClientChan: make(chan struct{}),
	}

	return ser, ser.init()
}

// MakeDsn - creates dsn from config
func MakeDsn(c *Config) string {
	u := url.URL{Scheme: "amqp", User: url.UserPassword(c.User, c.Password), Host: c.Address, Path: "/" + c.VirtualHost}
	return u.String()
}

func (si *SubscribeInfo) KeyName() string {
	return strings.Replace(si.Queue, "_", ".", -1)
}

func (s *Server) init() error {
	err := s.reconnect()
	if err != nil {
		return err
	}

	return nil
}

func (s *Server) reconnect() error {
	s.connLock.Lock()
	defer s.connLock.Unlock()

	conn := newConnection(s.config, s)
	err := conn.connect()
	if err != nil {
		return err
	}
	s.conn = conn

	return nil
}

func (s *Server) getConnection() *connection {
	s.connLock.RLock()
	defer s.connLock.RUnlock()

	return s.conn
}

func (s *Server) onCloseWithErr(conn *connection, err error) {

	log.Warnf("AMQP: connection closed, err %v", err)

	for {

		select {
		case <-s.stopClientChan:
			return
		default:
			<-time.After(5 * time.Second)
			err = s.reconnect()
			if err != nil {
				log.Warnf("AMQP: reconnection err %v", err)
			} else {
				return
			}
		}
	}
}

func (s *Server) Serve() (err error) {
	subscribers := make(map[string]*SubscribeInfo)
	for _, si := range s.subs {
		if _, ok := subscribers[si.Queue]; ok {
			return fmt.Errorf("amqp_kit: duplicate queue entry: '%s' ", si.Queue)
		}

		subscribers[si.Queue] = &si
	}

	for _, sub := range subscribers {
		for i := 0; i <= sub.Workers; i++ {

			go func(si *SubscribeInfo) {
				if err := s.receive(si); err != nil {
					log.Error(`receive err: %s`, err)
				}
			}(sub)
		}
	}

	return
}

func (s *Server) receive(si *SubscribeInfo) error {
	for {
		conn := s.getConnection()
		ch, err := conn.getChan()
		if err != nil {
			return fmt.Errorf("AMQP: Channel err: %s", err.Error())
		}

		msgs, err := ch.c.Consume(si.Queue, "", false, false, false, false, nil)
		if err != nil {
			return fmt.Errorf(" Channel consume err: %s", err.Error())
		}
		fun := NewSubscriber(si.E, si.Dec, si.Enc, si.O...).ServeDelivery(ch.c)

		var d amqp.Delivery
		var ok bool

		for {
			select {
			case d, ok = <-msgs:
			}
			if !ok {
				return fmt.Errorf(" close channel err")
			}

			fun(&d)
		}
	}
}

//func (s *Server) reconnect(count int, exchange, queue string, keys ...string) (ch *amqp.Channel, err error) {
//
//	ch, err = s.Conn.Channel()
//	if err != nil {
//		return
//	}
//
//	if err = ch.ExchangeDeclare(
//		exchange, // name
//		"topic",  // type
//		true,     // durable
//		false,    // auto-deleted
//		false,    // internal
//		false,    // no-wait
//		nil,      // arguments
//	); err != nil {
//		return
//	}
//
//	var q amqp.Queue
//
//	if q, err = ch.QueueDeclare(
//		queue, // name
//		true,  // durable
//		false, // delete when unused
//		false, // exclusive
//		false, // no-wait
//		nil,   // arguments
//	); err != nil {
//		return
//	}
//
//	if err = ch.Qos(
//		count, // prefetch count
//		0,     // prefetch size
//		false, // global
//	); err != nil {
//		return
//	}
//
//	for _, key := range keys {
//		if err = ch.QueueBind(
//			q.Name,   // queue name
//			key,      // routing key
//			exchange, // exchange
//			false,
//			nil,
//		); err != nil {
//			return
//		}
//	}
//
//	return ch, nil
//}

//func (s *Server) Stop() error {
//	return s.ch.Close()
//}

/*
func Declare(ch *amqp.Channel, exchange string, queue string, keys []string) error {
	if err := ch.ExchangeDeclare(
		exchange, // name
		"topic",  // type
		true,     // durable
		false,    // auto-deleted
		false,    // internal
		false,    // no-wait
		nil,      // arguments
	); err != nil {
		return err
	}

	q, err := ch.QueueDeclare(
		queue, // name
		true,  // durable
		false, // delete when unused
		false, // exclusive
		false, // no-wait
		nil,   // arguments
	)
	if err != nil {
		return err
	}

	if err := ch.Qos(
		1,     // prefetch count
		0,     // prefetch size
		false, // global
	); err != nil {
		return err
	}

	for _, key := range keys {
		if err := ch.QueueBind(
			q.Name,   // queue name
			key,      // routing key
			exchange, // exchange
			false,
			nil,
		); err != nil {
			return err
		}
	}

	return nil
}
*/

// Close closes the existed connection
func (s *Server) Close() error {
	s.connLock.Lock()
	defer s.connLock.Unlock()

	close(s.stopClientChan)
	if s.conn != nil {
		return s.conn.close()
	}

	return nil
}

// Publish publishing some message to given exchange
func (s *Server) Publish(exchange, key, corID string, body []byte) (err error) {
	// add retry
	conn := s.getConnection()
	channel, err := conn.getChan()
	if err != nil {
		return fmt.Errorf("AMQP: Channel err: %s", err.Error())
	}
	defer conn.putChan(channel)

	msg := amqp.Publishing{
		ContentType:   "application/json",
		CorrelationId: corID,
		Body:          body,
		DeliveryMode:  amqp.Persistent,
	}

	if err = channel.c.Publish(exchange, key, false, false, msg); err != nil {
		channel.err = err
		return fmt.Errorf("AMQP: Exchange Publish err: %s", err.Error())
	}

	return nil
}
