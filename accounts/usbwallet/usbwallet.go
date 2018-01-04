
// Package usbwallet implements support for USB hardware wallets.
package usbwallet

// deviceID is a combined vendor/product identifier to uniquely identify a USB
// hardware device.
type deviceID struct {
	Vendor  uint16 // The Vendor identifer
	Product uint16 // The Product identifier
}
