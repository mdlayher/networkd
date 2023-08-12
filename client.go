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

	// methodGet fetches all of an object's D-Bus properties.
	methodGetAll = "org.freedesktop.DBus.Properties.GetAll"
)

// A Client can issue D-Bus requests to systemd-networkd.
type Client struct {
	Manager *ManagerService

	// Functions which normally manipulate D-Bus but are also swappable for
	// tests.
	c      *dbus.Conn
	call   callFunc
	get    getFunc
	getAll getAllFunc
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
		c:      conn,
		call:   makeCall(conn),
		get:    makeGet(conn),
		getAll: makeAllGet(conn),
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

	c.Manager = &ManagerService{c: c}
	return c, nil
}

// The ManagerService exposes methods and properties of the networkd Manager
// object.
type ManagerService struct {
	c *Client
}

// ManagerProperties contains all of the D-Bus properties for the networkd
// Manager object.
type ManagerProperties struct {
	OperationalState string
	CarrierState     string
	AddressState     string
	IPv4AddressState string
	IPv6AddressState string
	OnlineState      string
}

// Properties fetches all D-Bus properties for the networkd Manager object.
func (ms *ManagerService) Properties(ctx context.Context) (ManagerProperties, error) {
	out, err := ms.c.getAll(ctx, objectPath(), interfacePath("Manager"))
	if err != nil {
		return ManagerProperties{}, err
	}

	return ManagerProperties{
		OperationalState: out["OperationalState"].Value().(string),
		CarrierState:     out["CarrierState"].Value().(string),
		AddressState:     out["AddressState"].Value().(string),
		IPv4AddressState: out["IPv4AddressState"].Value().(string),
		IPv6AddressState: out["IPv6AddressState"].Value().(string),
		OnlineState:      out["OnlineState"].Value().(string),
	}, nil
}

// A Link is a network link known to systemd-networkd.
type Link struct {
	Index      int
	Name       string
	ObjectPath dbus.ObjectPath
}

// ListLinks lists all of the network links known to systemd-networkd.
func (ms *ManagerService) ListLinks(ctx context.Context) ([]Link, error) {
	var m dbus.Variant
	err := ms.c.call(ctx, dbusCall{
		Service: interfacePath(),
		Object:  objectPath(),
		Method:  interfacePath("Manager.ListLinks"),
		Out:     &m,
	})
	if err != nil {
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

type dbusCall struct {
	Service string
	Object  dbus.ObjectPath
	Method  string
	Out     any
	Args    []any
}

// A callFunc is a function which calls a D-Bus method on an object and
// optionally stores its output in the pointer provided to out.
type callFunc func(ctx context.Context, call dbusCall) error

// A getFunc is a function which fetches a D-Bus property from an object.
type getFunc func(ctx context.Context, op dbus.ObjectPath, iface, prop string) (dbus.Variant, error)

// A getAllFunc is a function which fetches all D-Bus properties for an object.
type getAllFunc func(ctx context.Context, op dbus.ObjectPath, iface string) (map[string]dbus.Variant, error)

// makeCall produces a callFunc which calls a D-Bus method on an object.
func makeCall(c *dbus.Conn) callFunc {
	return func(ctx context.Context, call dbusCall) error {
		dCall := c.Object(call.Service, call.Object).CallWithContext(ctx, call.Method, 0, call.Args...)
		if dCall.Err != nil {
			return fmt.Errorf("call %q: %w", call.Method, dCall.Err)
		}

		// Store the results of the call only when out is not nil.
		if call.Out == nil {
			return nil
		}

		return dCall.Store(call.Out)
	}
}

// makeGet produces a getFunc which can fetch an object's property from a D-Bus
// interface.
func makeGet(c *dbus.Conn) getFunc {
	// Adapt a getFunc using the more generic callFunc.
	call := makeCall(c)
	return func(ctx context.Context, op dbus.ObjectPath, iface, prop string) (dbus.Variant, error) {
		var out dbus.Variant
		err := call(ctx, dbusCall{
			Service: baseService,
			Object:  op,
			Method:  methodGet,
			Out:     &out,
			Args:    []any{iface, prop},
		})
		if err != nil {
			return dbus.Variant{}, fmt.Errorf("get property %q for %q: %w", prop, iface, err)
		}

		return out, nil
	}
}

// makeGetAll produces a getAllFunc which can fetch all of an object's
// properties from a D-Bus interface.
func makeAllGet(c *dbus.Conn) getAllFunc {
	// Adapt a getAllFunc using the more generic callFunc.
	call := makeCall(c)
	return func(ctx context.Context, op dbus.ObjectPath, iface string) (map[string]dbus.Variant, error) {
		var out map[string]dbus.Variant
		err := call(ctx, dbusCall{
			Service: baseService,
			Object:  op,
			Method:  methodGetAll,
			Out:     &out,
			Args:    []any{iface},
		})
		if err != nil {
			return nil, fmt.Errorf("get all properties for %q: %w", iface, err)
		}

		return out, nil
	}
}

func panicf(format string, a ...any) {
	panic(fmt.Sprintf(format, a...))
}
