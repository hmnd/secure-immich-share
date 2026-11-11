declare global {
	interface Window {
		showDirectoryPicker?(options?: {
			id?: string;
			mode?: 'read' | 'readwrite';
			startIn?: 'desktop' | 'documents' | 'downloads' | 'music' | 'pictures' | 'videos';
		}): Promise<FileSystemDirectoryHandle>;
	}
}

export {};
