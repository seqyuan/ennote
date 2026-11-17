package globalsource

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/seqyuan/ennote/ennoworker/internal/agentflow"
	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/heroesource"
)

// MutateGraphWithLocalHero applies one draft mutation to graph.yaml and, when
// hero is non-nil, creates the Graph-local Hero draft the mutation refers to.
//
// Both writes happen under ONE source lock and in one order: the Hero first,
// then the definition. The reverse order would let a failed Hero write leave a
// graph.yaml that references a Hero nothing ever created, which is the state the
// "Add Task" path used to reach by hand. A definition failure removes the Hero
// this call created, so the draft is never half-applied; a Hero that already
// existed is never touched.
//
// Creating is idempotent by content: replaying the same request (a lost response,
// a client retry) finds the identical draft and succeeds. A same-handle Hero with
// different content is refused rather than overwritten — the caller asked to
// create, not to replace.
func (s Store) MutateGraphWithLocalHero(graphID, expectedDigest string, hero *heroesource.Document,
	mutate func(*domain.FlowDefinition) error) (*heroesource.Document, *domain.FlowDefinition, string, error) {
	unlock := lockSource(filepath.Join(s.GraphsDir(), graphID))
	defer unlock()

	definition, digest, err := s.ReadGraphDefinition(graphID)
	if err != nil {
		return nil, nil, "", err
	}
	if expectedDigest == "" || expectedDigest != digest {
		return nil, nil, digest, ErrConflict
	}

	createdHeroDir := ""
	if hero != nil {
		if err := heroesource.ValidateForWrite(hero); err != nil {
			return nil, nil, digest, err
		}
		encoded, err := heroesource.Encode(hero)
		if err != nil {
			return nil, nil, digest, err
		}
		wanted, err := heroesource.SourceDigest(hero)
		if err != nil {
			return nil, nil, digest, err
		}
		_, existing, readErr := s.ReadGraphHero(graphID, hero.Handle)
		switch {
		case readErr == nil && existing == wanted:
			// Replay of a request that already landed.
		case readErr == nil:
			return nil, nil, digest, fmt.Errorf("local Hero %q already exists with a different definition", hero.Handle)
		case errors.Is(readErr, os.ErrNotExist):
			heroesDir, err := ensureSourceDir(filepath.Join(s.GraphsDir(), graphID), "heroes")
			if err != nil {
				return nil, nil, digest, err
			}
			heroDir, err := createSourceDir(heroesDir, hero.Handle)
			if err != nil {
				return nil, nil, digest, err
			}
			if err := atomicWrite(filepath.Join(heroDir, "hero.md"), encoded, true); err != nil {
				_ = os.Remove(heroDir)
				return nil, nil, digest, err
			}
			createdHeroDir = heroDir
		default:
			return nil, nil, digest, readErr
		}
	}

	if mutate == nil {
		removeCreatedHeroDir(createdHeroDir)
		return nil, nil, digest, errors.New("Graph mutation is required")
	}
	if err := mutate(definition); err != nil {
		removeCreatedHeroDir(createdHeroDir)
		return nil, nil, digest, err
	}
	if definition.ID != graphID {
		removeCreatedHeroDir(createdHeroDir)
		return nil, nil, digest, errors.New("Graph id cannot be changed")
	}
	canonicalizeTaskTypes(definition)
	encoded, err := agentflow.DefinitionToYAML(definition)
	if err != nil {
		removeCreatedHeroDir(createdHeroDir)
		return nil, nil, digest, err
	}
	graphPath, _ := objectPath(s.GraphsDir(), graphID, graphSourceName)
	if err := atomicWrite(graphPath, encoded, false); err != nil {
		removeCreatedHeroDir(createdHeroDir)
		return nil, nil, digest, err
	}
	newDigest, err := AgentFlowDigest(definition)
	if err != nil {
		return nil, nil, digest, err
	}
	return hero, definition, newDigest, nil
}

// removeCreatedHeroDir undoes the Hero half of a failed mutation. It is
// best-effort on purpose: a directory that survives is an unreferenced draft the
// reader can see and delete, never a Task pointing at nothing.
func removeCreatedHeroDir(directory string) {
	if directory == "" {
		return
	}
	_ = os.RemoveAll(directory)
}

// UpdateGraphHero applies one whole-document CAS update to a Graph-private Hero
// draft. The handle is the directory identity and can never change in place.
func (s Store) UpdateGraphHero(graphID, heroID, expectedDigest string,
	mutate func(*heroesource.Document) error) (*heroesource.Document, string, error) {
	unlock := lockSource(filepath.Join(s.GraphsDir(), graphID, "heroes", heroID))
	defer unlock()

	document, digest, err := s.ReadGraphHero(graphID, heroID)
	if err != nil {
		return nil, "", err
	}
	if expectedDigest == "" || expectedDigest != digest {
		return nil, digest, ErrConflict
	}
	if mutate == nil {
		return nil, digest, errors.New("Hero mutation is required")
	}
	if err := mutate(document); err != nil {
		return nil, digest, err
	}
	if document.Handle != heroID {
		return nil, digest, errors.New("Hero handle cannot be changed")
	}
	encoded, err := heroesource.Encode(document)
	if err != nil {
		return nil, digest, err
	}
	directory, err := s.GraphResourceDir(graphID, "heroes", heroID)
	if err != nil {
		return nil, digest, err
	}
	if err := atomicWrite(filepath.Join(directory, "hero.md"), encoded, false); err != nil {
		return nil, digest, err
	}
	newDigest, err := heroesource.SourceDigest(document)
	if err != nil {
		return nil, digest, err
	}
	return document, newDigest, nil
}

// GraphHeroPath is the hero.md path a reader can open for a Graph-private Hero.
// It is derived, never stored, so a moved Graph home stays consistent.
func (s Store) GraphHeroPath(graphID, heroID string) string {
	return filepath.Join(s.GraphsDir(), graphID, "heroes", heroID, "hero.md")
}
