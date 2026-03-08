//go:build tinygo

package tinygorpiw

// TODO: Register with TinyGo's net package once the netdev interface
// is available in the target TinyGo version.
func registerNetdev(nd *NetDev) {}
