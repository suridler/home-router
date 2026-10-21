// Package geodatalimits defines the per-file bound shared by geodata operations.
package geodatalimits

import "fmt"

const BytesPerMB = 1024 * 1024

func ValidMB(mb int) bool { return mb > 0 && mb < int(^uint(0)>>1)/BytesPerMB }

// Bytes converts a validated configured size; it does not supply defaults.
func Bytes(mb int) int { return mb * BytesPerMB }

// Exceeded carries only the safe configured limit, never paths or process output.
type Exceeded struct{ LimitBytes int }

func (e *Exceeded) Error() string {
	return fmt.Sprintf("Размер файла геобазы превышает заданный лимит — %d МБ", e.LimitBytes/BytesPerMB)
}
