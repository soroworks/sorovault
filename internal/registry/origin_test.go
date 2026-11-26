package registry_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/stellar"
)

func TestLocateOrigin(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("happy path finds and records origin", func(t *testing.T) {
		t.Parallel()
		reg, fake, db := newRegistry(t)
		fake.Deploy(idA, moduleWith(t, "hello"))
		fake.SetOrigin(idA, "GCreatorAddress", 1234)

		// Register contract first so it exists in store
		_, err := reg.Register(ctx, idA)
		require.NoError(t, err)

		found, err := reg.LocateOrigin(ctx, idA)
		require.NoError(t, err)
		assert.True(t, found)

		got, err := db.GetContract(ctx, "testnet", idA)
		require.NoError(t, err)
		assert.Equal(t, "GCreatorAddress", got.Creator)
		assert.Equal(t, uint32(1234), got.CreatedLedger)
	})

	t.Run("table driven error and edge paths", func(t *testing.T) {
		t.Parallel()

		tests := []struct {
			name       string
			contractID string
			originErr  error
			wantFound  bool
			wantError  bool
		}{
			{
				name:       "unknown origin returns false and no error",
				contractID: idA,
				originErr:  stellar.ErrOriginUnknown,
				wantFound:  false,
				wantError:  false,
			},
			{
				name:       "rpc error returns error",
				contractID: idA,
				originErr:  errors.New("rpc failure"),
				wantFound:  false,
				wantError:  true,
			},
			{
				name:       "invalid contract ID returns error",
				contractID: "bad-id",
				wantFound:  false,
				wantError:  true,
			},
		}

		for _, tt := range tests {
			tt := tt
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				reg, fake, _ := newRegistry(t)
				if tt.contractID == idA {
					fake.Deploy(idA, moduleWith(t, "hello"))
				}
				fake.OriginErr = tt.originErr

				found, err := reg.LocateOrigin(ctx, tt.contractID)
				if tt.wantError {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
				}
				assert.Equal(t, tt.wantFound, found)
			})
		}
	})
}
