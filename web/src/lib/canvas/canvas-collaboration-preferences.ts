const CURSORS_KEY = "canvas-collaboration-show-remote-cursors";

// Display preference for this client, deliberately outside the shared document.
export function readRemoteCursorsVisible(): boolean {
    try {
        return window.localStorage.getItem(CURSORS_KEY) !== "false";
    } catch {
        return true;
    }
}

export function persistRemoteCursorsVisible(visible: boolean) {
    try {
        window.localStorage.setItem(CURSORS_KEY, String(visible));
    } catch {
        // Keep the session choice when storage is disabled.
    }
}
