import { createRoot } from "react-dom/client";
import { ConfigProvider, theme } from "antd";
import { CanvasCollaborationMembers } from "../../src/pages/canvas/canvas-collaboration-members";

const dark = document.documentElement.dataset.theme === "dark";
createRoot(document.getElementById("root")!).render(
    <ConfigProvider theme={{ algorithm: dark ? theme.darkAlgorithm : theme.defaultAlgorithm }}>
        <div style={{ overflow: "auto", width: "100%" }}>
            <CanvasCollaborationMembers
                members={[
                    { peerId: "a", nickname: "SistinePC", state: {}, collaborating: true, online: true },
                    { peerId: "b", nickname: "DESKTOP-0C00SUT", state: {}, collaborating: true, online: true },
                ]}
                assetReadiness={null}
            />
        </div>
        <button id="outside">Outside canvas</button>
    </ConfigProvider>,
);
