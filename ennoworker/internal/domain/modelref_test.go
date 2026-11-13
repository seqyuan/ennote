package domain

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSplitPortableModelRef(t *testing.T) {
	tests := []struct {
		name         string
		ref          string
		wantProvider string
		wantModel    string
		wantErr      bool
	}{
		{name: "two segments", ref: "provider/model", wantProvider: "provider", wantModel: "model"},
		{name: "multi segment model", ref: "openai-main/org/gpt-5.4", wantProvider: "openai-main", wantModel: "org/gpt-5.4"},
		{name: "empty model", ref: "provider/", wantErr: true},
		{name: "empty provider", ref: "/model", wantErr: true},
		{name: "no separator", ref: "provider", wantErr: true},
		{name: "whitespace in provider", ref: "provider /model", wantErr: true},
		{name: "whitespace in model", ref: "provider/ model", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider, model, err := SplitPortableModelRef(test.ref)
			if test.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.wantProvider, provider)
			require.Equal(t, test.wantModel, model)
		})
	}
}
