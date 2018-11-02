package main

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/lib/pq"
)

type Object struct {
	CID      string         `json:"cid" db:"cid"`
	SizeGB   float64        `json:"sizegb" db:"sizegb"`
	PinnedAt time.Time      `json:"pinned_at" db:"pinned_at"`
	EndsAt   time.Time      `json:"ends_at" db:"ends_at"`
	Notes    pq.StringArray `json:"notes" db:"notes"`
}

type Payment struct {
	OrderId   string `json:"order_id" db:"order_id"`
	CID       string `json:"cid" db:"cid"`
	PaidAt    string `json:"paid_at" db:"paid_at"`
	Amount    int    `json:"amount" db:"amount"`
	Processed bool   `json:"processed" db:"processed"`
	Tries     int    `json:"tries" db:"tries"`
	GivenUp   bool   `json:"given_up" db:"given_up"`
}

func fetchObjects() (oo []Object, err error) {
	oo = make([]Object, 0)
	err = pg.Select(&oo, `
SELECT cid, sizegb, pinned_at, pinned_at + lifespan AS ends_at, notes(o)
FROM objects AS o
ORDER BY ends_at ASC
    `)
	return
}

func fetchObject(cid string) (*Object, error) {
	o := Object{}
	err = pg.Get(&o, `
SELECT cid, sizegb, pinned_at, pinned_at + lifespan AS ends_at, notes(o)
FROM objects AS o WHERE cid = $1
    `, cid)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &o, err
}

func fetchPayment(orderId string) (*Payment, error) {
	p := Payment{}
	err = pg.Get(&p, `
SELECT order_id, paid_at, amount, processed, tries, given_up
FROM payments AS o WHERE order_id = $1
    `, orderId)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &p, err
}

func savePayment(order_id, cid string, amount int, note string) error {
	_, err := pg.Exec(`
INSERT INTO payments (order_id, cid, amount, note)
VALUES ($1, $2, $3, $4)
    `, order_id, cid, amount, note)
	return err
}

func processPayments() error {
	var payments []struct {
		OrderId string `db:"order_id"`
		CID     string `db:"cid"`
		Amount  int64  `db:"amount"`
	}
	err = pg.Select(&payments, `
WITH giveup AS (
  UPDATE payments
  SET given_up = true, processed = true
  WHERE tries > 5
)
UPDATE payments
SET tries = payments.tries + 1
WHERE NOT processed
RETURNING order_id, cid, amount
    `)
	if err != nil {
		return err
	}

	jobs := make(chan error, len(payments))
	for _, payment := range payments {
		go func(orderId string, cid string, amount int64) {
			log.Debug().Str("order_id", orderId).Int64("amount", amount).Str("cid", cid).
				Msg("processing payment")
			sizegb, err := pin(
				cid,
				float64(payment.Amount)/float64(s.PriceGB),
			)
			if err != nil {
				jobs <- err
				return
			}

			duration := time.Hour * time.Duration(
				float64(amount)/float64(s.PriceGB/24)/sizegb,
			)

			_, err = pg.Exec(`
WITH c AS (
  UPDATE payments
  SET processed = true
  WHERE order_id = $1
)
INSERT INTO objects (cid, sizegb, pinned_at, lifespan)
VALUES ($2, $3, now(), make_interval(secs := $4))
ON CONFLICT (cid)
  DO UPDATE SET lifespan = objects.lifespan + make_interval(secs := $4)
            `, orderId, cid, sizegb, duration.Seconds())
			jobs <- err
			return
		}(payment.OrderId, payment.CID, payment.Amount)
	}

	anyerr := make(chan error, 1)
	go func() {
		for err := range jobs {
			if err != nil {
				anyerr <- err
				return
			}
		}
		anyerr <- nil
	}()

	select {
	case res := <-anyerr:
		return res
	case <-time.After(60 * time.Minute):
		return errors.New("timeout")
	}
}

func eraseEnded() error {
	var cids []string
	err := pg.Select(&cids, `
SELECT cid FROM objects WHERE pinned_at + lifespan < now()
    `)
	if err != nil {
		return err
	}

	log.Debug().Str("cids", strings.Join(cids, ",")).Msg("erasing ended")
	for _, cid := range cids {
		err = unpin(cid)
		if err != nil {
			return err
		}

		_, err = pg.Exec(`
DELETE FROM objects WHERE cid = $1
        `, cid)
		if err != nil {
			return err
		}
	}

	return nil
}
