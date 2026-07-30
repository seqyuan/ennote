package store_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	stores "github.com/seqyuan/ennote/ennoworker/internal/store"
)

func BenchmarkMessagePage(b *testing.B) {
	for _, count := range []int{100, 500, 1000} {
		b.Run(fmt.Sprintf("messages_%d", count), func(b *testing.B) {
			db := stores.SetupDB(b)
			ctx := context.Background()
			projects := &stores.ProjectRepo{DB: db}
			sessions := &stores.SessionRepo{DB: db}
			messages := &stores.MessageRepo{DB: db}
			project, _, err := projects.CreateWithWorkspace(ctx, domain.CreateProjectInput{Name: "Benchmark", HostPath: b.TempDir()})
			if err != nil {
				b.Fatal(err)
			}
			session, err := sessions.Create(ctx, domain.CreateSessionInput{ProjectID: project.ID})
			if err != nil {
				b.Fatal(err)
			}
			parentID := ""
			for index := 0; index < count; index++ {
				message, createErr := messages.CreateUserMessage(ctx, session.ID, parentID, fmt.Sprintf("message %d", index))
				if createErr != nil {
					b.Fatal(createErr)
				}
				parentID = message.ID
			}
			if err := sessions.ActivateLeaf(ctx, session.ID, parentID); err != nil {
				b.Fatal(err)
			}

			b.ResetTimer()
			for iteration := 0; iteration < b.N; iteration++ {
				page, pageErr := messages.Page(ctx, session.ID, parentID, "", 50)
				if pageErr != nil || len(page.Messages) != 50 {
					b.Fatalf("page: messages=%d err=%v", len(page.Messages), pageErr)
				}
			}
		})
	}
}
