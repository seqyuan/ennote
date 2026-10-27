import type { Metadata } from "next";
import "./tokens.css";
import "./globals.css";
import "./sidebar.css";
import "./composer.css";
import "./settings.css";
import "./project-picker.css";
import { LocaleProvider } from "@/components/LocaleProvider";
import { WorkerGenerationGuard } from "@/components/WorkerGenerationGuard";
import { WorkspaceProvider } from "@/components/WorkspaceProvider";

const themeInitScript = `(function(){try{var t=localStorage.getItem("ennote-theme");var d=t==="dark"||(t!=="light"&&matchMedia("(prefers-color-scheme:dark)").matches);document.documentElement.classList.toggle("dark",d);document.documentElement.dataset.theme=d?"dark":"light"}catch(e){}})();`;
const localeInitScript = `(function(){try{var l=localStorage.getItem("ennote-locale");document.documentElement.lang=(l==="zh-CN")?"zh-CN":"en"}catch(e){}})();`;

export const metadata: Metadata = {
  title: "Ennote",
  description: "Auditable local agent runtime built around Agent Flow",
};

export default function RootLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    <html lang="en" suppressHydrationWarning>
      <head>
        <script
          id="theme-init"
          dangerouslySetInnerHTML={{ __html: themeInitScript }}
        />
        <script
          id="locale-init"
          dangerouslySetInnerHTML={{ __html: localeInitScript }}
        />
      </head>
      <body style={{ height: "100dvh", display: "flex", flexDirection: "column" }}>
        <WorkerGenerationGuard>
          <LocaleProvider>
            <WorkspaceProvider>{children}</WorkspaceProvider>
          </LocaleProvider>
        </WorkerGenerationGuard>
      </body>
    </html>
  );
}
