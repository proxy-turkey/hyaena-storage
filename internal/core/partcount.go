// Package core, dil-bağımsız saf mantığı barındırır (parça hesaplama, güvenlik).
// Kanal dağıtımı (PickUploadChannelsBalanced) artık storage paketinde kalıcı sayaçla yapılır.
package core

// PartCount, bir dosyanın segment sayısını döndürür: max(1, ceil(size/segment)).
func PartCount(size, segment int64) int {
	if segment <= 0 {
		segment = 20 * 1024 * 1024
	}
	n := int((size + segment - 1) / segment)
	if n < 1 {
		return 1
	}
	return n
}
