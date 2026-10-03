//go:build !windows

package tun

func tunPlatformReady(interfaceName string, expectedAddress string) bool {
	_, ready := tunInterfaceWithExpectedAddress(interfaceName, expectedAddress)
	return ready
}
