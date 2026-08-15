import type { Metadata } from "next";
import "./globals.css";
import "./sidebar.css";
import "./composer.css";
import { WorkerGenerationGuard } from "@/components/WorkerGenerationGuard";
import { WorkspaceProvider } from "@/components/WorkspaceProvider";

const themeInitScript = `(function(){try{var t=localStorage.getItem("ennote-theme");var d=t==="dark"||(t!=="light"&&matchMedia("(prefers-color-scheme:dark)").matches);document.documentElement.classList.toggle("dark",d);document.documentElement.dataset.theme=d?"dark":"light"}catch(e){}})();`;

export const metadata: Metadata = {
  title: "Ennote",
  description: "AI-native bioinformatics agent workspace",
};

export default function RootLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    <html lang="zh-CN" suppressHydrationWarning>
      <head>
        <script
          id="theme-init"
          dangerouslySetInnerHTML={{ __html: themeInitScript }}
        />
      </head>
      <body style={{ height: "100dvh", display: "flex", flexDirection: "column" }}>
        <WorkerGenerationGuard>
          <WorkspaceProvider>{children}</WorkspaceProvider>
        </WorkerGenerationGuard>
      </body>
    </html>
  );
}
