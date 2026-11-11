// Hooks the tests install in pages with addInitScript.
interface Window {
	showDirectoryPicker?: () => Promise<FileSystemDirectoryHandle>;
	/** Rejects a pending stubbed folder picker as if the visitor closed it. */
	cancelPicker?: () => void;
}
