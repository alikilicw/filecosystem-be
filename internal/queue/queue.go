package queue

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/alikilicw/filecosystem-be/internal/contracts"
	amqp "github.com/rabbitmq/amqp091-go"
)

var ErrNotConnected = errors.New("queue: not connected")

const (
	reconnectDelay = 3 * time.Second
	startupTimeout = 60 * time.Second
)

// Handler processes a single delivery. Returning an error nacks the message
// without requeueing it, which keeps a poison message from looping forever.
type Handler func(ctx context.Context, body []byte) error

type consumerSpec struct {
	queue       string
	prefetch    int
	parallelism int
	handler     Handler
}

// Client owns one AMQP connection and reconnects in the background, so callers
// never have to care about a broker restart.
type Client struct {
	url string
	log *slog.Logger

	mu        sync.RWMutex
	conn      *amqp.Connection
	ch        *amqp.Channel
	consumers []consumerSpec

	pubMu sync.Mutex
	wg    sync.WaitGroup
}

func New(url string, log *slog.Logger) *Client {
	return &Client{url: url, log: log}
}

// RegisterConsumer must be called before Start. Consumers are re-attached
// automatically after every reconnect. parallelism is how many deliveries the
// consumer handles at the same time.
func (c *Client) RegisterConsumer(queue string, prefetch, parallelism int, handler Handler) {
	if parallelism < 1 {
		parallelism = 1
	}
	c.consumers = append(c.consumers, consumerSpec{
		queue:       queue,
		prefetch:    prefetch,
		parallelism: parallelism,
		handler:     handler,
	})
}

// Start blocks until the broker accepts a connection or startupTimeout passes,
// which tolerates a broker that is still booting, then keeps the connection
// alive until ctx is cancelled.
func (c *Client) Start(ctx context.Context) error {
	deadline := time.Now().Add(startupTimeout)
	for {
		err := c.connect(ctx)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			return err
		}
		c.log.Warn("waiting for rabbitmq", "error", err)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(reconnectDelay):
		}
	}

	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.supervise(ctx)
	}()
	return nil
}

func (c *Client) Close() {
	c.wg.Wait()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ch != nil {
		_ = c.ch.Close()
	}
	if c.conn != nil {
		_ = c.conn.Close()
	}
}

func (c *Client) supervise(ctx context.Context) {
	for {
		c.mu.RLock()
		conn := c.conn
		c.mu.RUnlock()

		var closed chan *amqp.Error
		if conn != nil {
			closed = conn.NotifyClose(make(chan *amqp.Error, 1))
		}

		select {
		case <-ctx.Done():
			return
		case err := <-closed:
			c.log.Warn("rabbitmq connection lost, reconnecting", "error", err)
		}

		for ctx.Err() == nil {
			if err := c.connect(ctx); err != nil {
				c.log.Warn("rabbitmq reconnect failed", "error", err)
				select {
				case <-ctx.Done():
					return
				case <-time.After(reconnectDelay):
				}
				continue
			}
			c.log.Info("rabbitmq reconnected")
			break
		}
	}
}

func (c *Client) connect(ctx context.Context) error {
	conn, err := amqp.Dial(c.url)
	if err != nil {
		return fmt.Errorf("dial rabbitmq: %w", err)
	}

	ch, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("open channel: %w", err)
	}

	if err := DeclareTopology(ch); err != nil {
		_ = conn.Close()
		return err
	}

	c.mu.Lock()
	c.conn, c.ch = conn, ch
	c.mu.Unlock()

	for _, spec := range c.consumers {
		if err := c.startConsumer(ctx, conn, spec); err != nil {
			_ = conn.Close()
			return err
		}
	}
	return nil
}

func (c *Client) startConsumer(ctx context.Context, conn *amqp.Connection, spec consumerSpec) error {
	ch, err := conn.Channel()
	if err != nil {
		return fmt.Errorf("open consumer channel: %w", err)
	}
	if err := ch.Qos(spec.prefetch, 0, false); err != nil {
		return fmt.Errorf("set qos: %w", err)
	}

	deliveries, err := ch.Consume(spec.queue, "", false, false, false, false, nil)
	if err != nil {
		return fmt.Errorf("consume %s: %w", spec.queue, err)
	}

	var workers sync.WaitGroup
	workers.Add(spec.parallelism)
	for i := 0; i < spec.parallelism; i++ {
		c.wg.Add(1)
		go func() {
			defer c.wg.Done()
			defer workers.Done()

			for {
				select {
				case <-ctx.Done():
					return
				case delivery, ok := <-deliveries:
					if !ok {
						return
					}
					if err := spec.handler(ctx, delivery.Body); err != nil {
						c.log.Error("message handling failed", "queue", spec.queue, "error", err)
						_ = delivery.Nack(false, false)
						continue
					}
					_ = delivery.Ack(false)
				}
			}
		}()
	}

	go func() {
		workers.Wait()
		_ = ch.Close()
	}()
	return nil
}

func (c *Client) Publish(ctx context.Context, exchange, routingKey string, body []byte) error {
	c.mu.RLock()
	ch := c.ch
	c.mu.RUnlock()
	if ch == nil || ch.IsClosed() {
		return ErrNotConnected
	}

	c.pubMu.Lock()
	defer c.pubMu.Unlock()

	return ch.PublishWithContext(ctx, exchange, routingKey, false, false, amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		Timestamp:    time.Now().UTC(),
		Body:         body,
	})
}

func (c *Client) Healthy() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.conn != nil && !c.conn.IsClosed()
}

// DeclareTopology is idempotent and run by both the API and the workers so
// whichever starts first provisions the broker.
func DeclareTopology(ch *amqp.Channel) error {
	exchanges := []string{contracts.ExchangeJobs, contracts.ExchangeEvents}
	for _, name := range exchanges {
		if err := ch.ExchangeDeclare(name, amqp.ExchangeTopic, true, false, false, false, nil); err != nil {
			return fmt.Errorf("declare exchange %s: %w", name, err)
		}
	}

	bindings := []struct{ queue, exchange, key string }{
		{contracts.QueueImageJobs, contracts.ExchangeJobs, contracts.RoutingKeyImage},
		{contracts.QueueJobResults, contracts.ExchangeEvents, contracts.RoutingKeyJobResult},
	}
	for _, b := range bindings {
		if _, err := ch.QueueDeclare(b.queue, true, false, false, false, nil); err != nil {
			return fmt.Errorf("declare queue %s: %w", b.queue, err)
		}
		if err := ch.QueueBind(b.queue, b.key, b.exchange, false, nil); err != nil {
			return fmt.Errorf("bind queue %s: %w", b.queue, err)
		}
	}
	return nil
}
