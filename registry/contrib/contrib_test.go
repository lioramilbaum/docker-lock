package contrib

import (
	"testing"

	"github.com/michaelperel/docker-lock/registry"
)

// TestAllWrappers ensures that all wrappers maintained by the
// community are returned.
func TestAllWrappers(t *testing.T) {
	client := &registry.HTTPClient{}

	wrappers, err := AllWrappers(client)
	if err != nil {
		t.Fatal("could not get wrappers")
	}

	expectedNumWrappers := 2
	numWrappers := len(wrappers)

	if numWrappers != expectedNumWrappers {
		t.Fatalf("got '%d' wrappers, want '%d'",
			numWrappers,
			expectedNumWrappers,
		)
	}

	if _, ok := wrappers[0].(*ElasticWrapper); !ok {
		t.Fatal("expected ElasticWrapper")
	}

	if _, ok := wrappers[1].(*MCRWrapper); !ok {
		t.Fatal("expected MCRWrapper")
	}
}
