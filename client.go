package networkd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/godbus/dbus/v5"
)

const (
	// baseService is the fixed base service name for networkd.
	baseService = "org.freedesktop.network1"

	// baseObject is the fixed base object path for networkd.
	baseObject = dbus.ObjectPath("/org/freedesktop/network1")

	// methodGet fetches a single D-Bus property by name.
	methodGet = "org.freedesktop.DBus.Properties.Get"
)

// A Client can issue D-Bus requests to systemd-networkd.
type Client struct {
	Version string

	// Functions which normally manipulate D-Bus but are also swappable for
	// tests.
	c    *dbus.Conn
	call callFunc
	get  getFunc
}

// Dial dials a D-Bus connection to systemd-networkd and returns a Client. If
// the service does not exist on the system bus, an error compatible with
// `errors.Is(err, os.ErrNotExist)` is returned.
func Dial(ctx context.Context) (*Client, error) {
	conn, err := dbus.SystemBus()
	if err != nil {
		return nil, err
	}

	return initClient(ctx, &Client{
		// Wrap the *dbus.Conn completely to abstract away all of the low-level
		// D-Bus logic for ease of unit testing.
		c:    conn,
		call: makeCall(conn),
		get:  makeGet(conn),
	})
}

// Close closes the underlying D-Bus connection.
func (c *Client) Close() error { return c.c.Close() }

// initClient verifies a Client can speak with systemd-networkd.
func initClient(ctx context.Context, c *Client) (*Client, error) {
	// See if the Manager object is available on the system bus.
	if _, err := c.get(ctx, objectPath(), interfacePath("Manager"), "OnlineState"); err != nil {
		return nil, toNotExist(err)
	}

	return c, nil
}

// A Link is a network link known to systemd-networkd.
type Link struct {
	Index      int
	Name       string
	ObjectPath dbus.ObjectPath
}

// ListLinks lists all of the network links known to systemd-networkd.
func (c *Client) ListLinks(ctx context.Context) ([]Link, error) {
	var m dbus.Variant
	if err := c.call(ctx, baseService, interfacePath("Manager.ListLinks"), objectPath(), &m); err != nil {
		return nil, err
	}

	var (
		values = m.Value().([][]any)
		links  = make([]Link, 0, len(values))
	)

	for _, vs := range values {
		if l := len(vs); l != 3 {
			return nil, fmt.Errorf("invalid number of link values: %d", l)
		}

		links = append(links, Link{
			Index:      int(vs[0].(int32)),
			Name:       vs[1].(string),
			ObjectPath: vs[2].(dbus.ObjectPath),
		})
	}

	return links, nil
}

// objectPath prepends its arguments with the base object path for networkd.
func objectPath(ss ...string) dbus.ObjectPath {
	p := dbus.ObjectPath(path.Join(
		// Prepend the base and join any further elements into one path.
		append([]string{string(baseObject)}, ss...)...,
	))

	// Since the paths in this program are effectively constant, they should
	// always be valid.
	if !p.IsValid() {
		panicf("networkd: bad D-Bus object path: %q", p)
	}

	return p
}

// interfacePath prepends its arguments with the base interface path for
// networkd.
func interfacePath(ss ...string) string {
	return strings.Join(append([]string{baseService}, ss...), ".")
}

// toNotExist wraps a D-Bus "no such unit" error with os.ErrNotExist for easy
// comparison.
func toNotExist(err error) error {
	var derr dbus.Error
	if !errors.As(err, &derr) || derr.Name != "org.freedesktop.systemd1.NoSuchUnit" {
		return err
	}

	return fmt.Errorf("%v: %w", err, os.ErrNotExist)
}

// A callFunc is a function which calls a D-Bus method on an object and
// optionally stores its output in the pointer provided to out.
type callFunc func(ctx context.Context, service, method string, op dbus.ObjectPath, out any, args ...any) error

// A getFunc is a function which fetches a D-Bus property from an object.
type getFunc func(ctx context.Context, op dbus.ObjectPath, iface, prop string) (dbus.Variant, error)

// makeCall produces a callFunc which calls a D-Bus method on an object.
func makeCall(c *dbus.Conn) callFunc {
	return func(ctx context.Context, service, method string, op dbus.ObjectPath, out any, args ...any) error {
		call := c.Object(service, op).CallWithContext(ctx, method, 0, args...)
		if call.Err != nil {
			return fmt.Errorf("call %q: %w", method, call.Err)
		}

		// Store the results of the call only when out is not nil.
		if out == nil {
			return nil
		}

		return call.Store(out)
	}
}

// makeGet produces a getFunc which can fetch an object's property from a D-Bus
// interface.
func makeGet(c *dbus.Conn) getFunc {
	// Adapt a getFunc using the more generic callFunc.
	call := makeCall(c)
	return func(ctx context.Context, op dbus.ObjectPath, iface, prop string) (dbus.Variant, error) {
		var out dbus.Variant
		if err := call(ctx, baseService, methodGet, op, &out, iface, prop); err != nil {
			return dbus.Variant{}, fmt.Errorf("get property %q for %q: %w", prop, iface, err)
		}

		return out, nil
	}
}

func panicf(format string, a ...any) {
	panic(fmt.Sprintf(format, a...))
}
