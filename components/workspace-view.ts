/**
 * Workspace main-area view. The sidebar always stays mounted; switching
 * the view only swaps what renders in the center+right region.
 *
 * - "chat":   normal chat workspace (TopBar + ChatWindow + RightPanel)
 * - "roles":  Roles settings editor (center+right become the settings page)
 * - "graphs": Graphs settings editor (center+right become the settings page)
 */
export type WorkspaceView = "chat" | "roles" | "graphs";
