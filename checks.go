package gochecks

import (
	"fmt"
	"net"
	"strings"
	"time"

	"net/url"

	"github.com/streadway/amqp"
	"github.com/tatsushid/go-fastping"
)

import (
	// mysql driver import
	_ "github.com/go-sql-driver/mysql"

	"database/sql"
)

const (
	maxPingTime = 1 * time.Second
)

// CheckFunction type for a function that return a event
type CheckFunction func() Event

// MultiCheckFunction type for a function that return an array of events
type MultiCheckFunction func() []Event

// Tags returns a new check function that adds the given tags to the result
// generated by the initial check function
func (f CheckFunction) Tags(tags ...string) CheckFunction {
	return func() Event {
		result := f()
		result.Tags = tags
		return result
	}
}

// Attributes returns a new check function that adds the attributes map to the result
// generated by the initial check function
func (f CheckFunction) Attributes(attributes map[string]string) CheckFunction {
	return func() Event {
		result := f()
		result.Attributes = attributes
		return result
	}
}

// TTL returns a new check function that adds the given TTL time (in seconds) to the result
// generated by the initial check function
func (f CheckFunction) TTL(ttl float32) CheckFunction {
	return func() Event {
		result := f()
		result.TTL = ttl
		return result
	}
}

// Retry returns a new check function that execute the given function up to a given retry times or
// until the first execution that returns a ok (whichever comes first). The new function will return
// the event of the last execution
func (f CheckFunction) Retry(times int, sleep time.Duration) CheckFunction {
	return func() Event {
		var result Event
		for i := 0; i < times; i++ {
			result = f()
			if result.State == "ok" {
				return result
			}
			time.Sleep(sleep)
		}
		return result
	}
}

// CriticalIfLessThan returns a new check function that change the state to "critical" when the resulting metric is less than a
// threadshold and is not already "critical"
func (f CheckFunction) CriticalIfLessThan(threshold float32) CheckFunction {
	return func() Event {
		var result Event
		result = f()
		if result.State == "critical" {
			return result
		}
		if result.Metric.(float32) < threshold {
			result.State = "critical"
			return result
		}
		return result
	}
}

// CriticalIfGreaterThan returns a new check function that change the state to "critical" when the resulting metric is greater than a
// threadshold and is not already "critical"
func (f CheckFunction) CriticalIfGreaterThan(threshold float32) CheckFunction {
	return func() Event {
		var result Event
		result = f()
		if result.State == "critical" {
			return result
		}
		if result.Metric.(float32) > threshold {
			result.State = "critical"
			return result
		}
		return result
	}
}

// WarningIfLessThan returns a new check function that change the state to "warning" when the resulting metric is less than a
// threadshold and is not already "critical"
func (f CheckFunction) WarningIfLessThan(threshold float32) CheckFunction {
	return func() Event {
		var result Event
		result = f()
		if result.State == "critical" {
			return result
		}
		if result.Metric.(float32) < threshold {
			result.State = "warning"
			return result
		}
		return result
	}
}

// WarningIfGreaterThan returns a new check function that change the state to "warning" when the resulting metric is greater than a
// threadshold and is not already "critical"
func (f CheckFunction) WarningIfGreaterThan(threshold float32) CheckFunction {
	return func() Event {
		var result Event
		result = f()
		if result.State == "critical" {
			return result
		}
		if result.Metric.(float32) > threshold {
			result.State = "warning"
			return result
		}
		return result
	}
}

// NewPingChecker returns a check function that can check if a host answer to a ICMP Ping
func NewPingChecker(host, service, ip string) CheckFunction {
	return func() Event {
		var retRtt time.Duration
		var result = Event{Host: host, Service: service, State: "critical"}

		p := fastping.NewPinger()
		p.MaxRTT = maxPingTime
		ra, err := net.ResolveIPAddr("ip4:icmp", ip)
		if err != nil {
			result.Description = err.Error()
		}

		p.AddIPAddr(ra)
		p.OnRecv = func(addr *net.IPAddr, rtt time.Duration) {
			result.State = "ok"
			result.Metric = float32(retRtt.Nanoseconds() / 1e6)
		}

		err = p.Run()
		if err != nil {
			result.Description = err.Error()
		}
		return result
	}
}

// NewTCPPortChecker returns a check function that can check if a host have a tcp port open
func NewTCPPortChecker(host, service, ip string, port int, timeout time.Duration) CheckFunction {
	return func() Event {
		var err error
		var conn net.Conn

		var t1 = time.Now()
		conn, err = net.DialTimeout("tcp", fmt.Sprintf("%s:%d", ip, port), timeout)
		if err == nil {
			conn.Close()
			milliseconds := float32((time.Now().Sub(t1)).Nanoseconds() / 1e6)
			return Event{Host: host, Service: service, State: "ok", Metric: milliseconds}
		}
		return Event{Host: host, Service: service, State: "critical"}
	}
}

// NewRabbitMQQueueLenCheck returns a check function that check if queue have more pending messages than a given limit
func NewRabbitMQQueueLenCheck(host, service, amqpuri, queue string, max int) CheckFunction {
	return func() Event {
		result := Event{Host: host, Service: service}

		conn, err := amqp.Dial(amqpuri)
		if err != nil {
			result.State = "critical"
			result.Description = err.Error()
			return result
		}

		ch, err := conn.Channel()
		if err != nil {
			result.State = "critical"
			result.Description = err.Error()
			return result
		}
		defer ch.Close()
		defer conn.Close()

		queueInfo, err := ch.QueueInspect(queue)
		if err != nil {
			result.State = "critical"
			result.Description = err.Error()
			return result
		}

		var state = "critical"
		if queueInfo.Messages <= max {
			state = "ok"
		}
		return Event{Host: host, Service: service, State: state, Metric: float32(queueInfo.Messages)}
	}
}

// NewMysqlConnectionCheck returns a check function to detect connection/credentials problems to connect to mysql
func NewMysqlConnectionCheck(host, service, mysqluri string) CheckFunction {
	return func() Event {
		u, err := url.Parse(mysqluri)
		if err != nil {
			return Event{Host: host, Service: service, State: "critical", Description: err.Error()}
		}

		if u.User == nil {
			return Event{Host: host, Service: service, State: "critical", Description: "No user defined"}
		}
		password, hasPassword := u.User.Password()
		if !hasPassword {
			return Event{Host: host, Service: service, State: "critical", Description: "No password defined"}
		}
		hostAndPort := u.Host
		if !strings.Contains(hostAndPort, ":") {
			hostAndPort = hostAndPort + ":3306"
		}
		var t1 = time.Now()
		con, err := sql.Open("mysql", u.User.Username()+":"+password+"@"+"tcp("+hostAndPort+")"+u.Path)
		defer con.Close()
		if err != nil {
			return Event{Host: host, Service: service, State: "critical", Description: err.Error()}
		}
		q := `select CURTIME()`
		row := con.QueryRow(q)
		var date string
		err = row.Scan(&date)
		milliseconds := float32((time.Now().Sub(t1)).Nanoseconds() / 1e6)
		if err != nil {
			return Event{Host: host, Service: service, State: "critical", Description: err.Error(), Metric: milliseconds}
		}
		return Event{Host: host, Service: service, State: "ok", Metric: milliseconds}
	}
}

// ObtainMetricFunction function that return a metric value or error
type ObtainMetricFunction func() (float32, error)

// CalculateStateFunction function that given a metric and error generate the corresponding state value and description
type CalculateStateFunction func(float32, error) (string, string)

// NewGenericCheck returns a check function that invoke a given function to obtain a metric (metricFunc) and
// invoke another function (stateFunc) to calculate the resulting state and description from this metric value
func NewGenericCheck(host, service string, metricFunc ObtainMetricFunction, stateFunc CalculateStateFunction) CheckFunction {
	return func() Event {
		value, err := metricFunc()
		var state, description = stateFunc(value, err)
		return Event{Host: host, Service: service, State: state, Metric: value, Description: description}
	}
}

func CriticalIfError(value float32, err error) (string, string) {
	if err != nil {
		return "critical", err.Error()
	}
	return "ok", ""
}
