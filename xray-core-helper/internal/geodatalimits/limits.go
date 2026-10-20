// Package geodatalimits defines the per-file bound shared by geodata operations.
package geodatalimits

import "fmt"

const DefaultAssetSizeMB = 256
const BytesPerMB = 1024 * 1024
const DefaultAssetSize = DefaultAssetSizeMB * BytesPerMB

func ValidMB(mb int) bool { return mb > 0 && mb < int(^uint(0)>>1)/BytesPerMB }

// Bytes resolves omitted internal options to the configured default.
func Bytes(mb int) int {
	if mb == 0 {
		mb = DefaultAssetSizeMB
	}
	return mb * BytesPerMB
}

// Exceeded carries only the safe configured limit, never paths or process output.
type Exceeded struct{ LimitBytes int }

func (e *Exceeded) Error() string {
	return fmt.Sprintf("Размер файла геобазы превышает заданный лимит — %d МБ", e.LimitBytes/BytesPerMB)
}
