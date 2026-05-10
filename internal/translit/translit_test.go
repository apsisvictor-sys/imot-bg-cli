package translit

import (
	"fmt"
	"testing"
)

func TestNeighborhoodSlugs(t *testing.T) {
	names := []string{"Лозенец", "Банишора", "Център", "Люлин", "Младост", "Изгрев", "Красно село", "Хиподрума"}
	for _, n := range names {
		fmt.Printf("%s -> %s\n", n, ToSlug(n))
	}
}
