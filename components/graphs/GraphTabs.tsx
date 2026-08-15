"use client";

import { Plus } from "lucide-react";
import type { GraphSummary } from "@/components/graphs/types";

export function GraphTabs({ graphs, selected, onSelect, onAdd }: {
  graphs: GraphSummary[];
  selected: string | null;
  onSelect: (id: string) => void;
  onAdd: () => void;
}) {
  return (
    <div className="graph-tabs-row">
      <div className="graph-tabs" role="tablist" aria-label="Graphs">
        {graphs.map((graph) => (
          <button
            key={graph.id}
            type="button"
            role="tab"
            aria-selected={selected === graph.id}
            className="graph-tab"
            onClick={() => onSelect(graph.id)}
          >
            <span>{graph.name}</span>
            {graph.error && <i title={graph.error}>!</i>}
          </button>
        ))}
      </div>
      <button type="button" className="graph-add-tab" onClick={onAdd} aria-label="Add Graph" title="Add Graph">
        <Plus size={18} />
      </button>
    </div>
  );
}
