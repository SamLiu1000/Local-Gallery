export namespace database {
	
	export class ImageTag {
	    imagePath: string;
	    tagId: string;
	    addedAt: string;
	
	    static createFrom(source: any = {}) {
	        return new ImageTag(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.imagePath = source["imagePath"];
	        this.tagId = source["tagId"];
	        this.addedAt = source["addedAt"];
	    }
	}
	export class ImportedRoot {
	    path: string;
	    name: string;
	    displayName: string;
	    folderType: string;
	    handleName: string;
	    addedAt: string;
	
	    static createFrom(source: any = {}) {
	        return new ImportedRoot(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.name = source["name"];
	        this.displayName = source["displayName"];
	        this.folderType = source["folderType"];
	        this.handleName = source["handleName"];
	        this.addedAt = source["addedAt"];
	    }
	}
	export class PromptVersion {
	    id: string;
	    imagePath: string;
	    positivePrompt: string;
	    negativePrompt: string;
	    source: string;
	    createdAt: string;
	
	    static createFrom(source: any = {}) {
	        return new PromptVersion(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.imagePath = source["imagePath"];
	        this.positivePrompt = source["positivePrompt"];
	        this.negativePrompt = source["negativePrompt"];
	        this.source = source["source"];
	        this.createdAt = source["createdAt"];
	    }
	}
	export class SearchResult {
	    id: string;
	    path: string;
	    name: string;
	    size: number;
	    lastModified: number;
	    createdAt: number;
	    folder: string;
	    rootPath: string;
	    prompt: string;
	    negativePrompt: string;
	    paramsJson: string;
	
	    static createFrom(source: any = {}) {
	        return new SearchResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.path = source["path"];
	        this.name = source["name"];
	        this.size = source["size"];
	        this.lastModified = source["lastModified"];
	        this.createdAt = source["createdAt"];
	        this.folder = source["folder"];
	        this.rootPath = source["rootPath"];
	        this.prompt = source["prompt"];
	        this.negativePrompt = source["negativePrompt"];
	        this.paramsJson = source["paramsJson"];
	    }
	}

}

export namespace main {
	
	export class SearchCondition {
	    field: string;
	    value: string;
	    mode: string;
	
	    static createFrom(source: any = {}) {
	        return new SearchCondition(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.field = source["field"];
	        this.value = source["value"];
	        this.mode = source["mode"];
	    }
	}
	export class AdvancedSearchRequest {
	    conditions: SearchCondition[];
	    folders: string[];
	    dateFrom: number;
	    dateTo: number;
	    matchMode: string;
	    offset: number;
	    limit: number;
	
	    static createFrom(source: any = {}) {
	        return new AdvancedSearchRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.conditions = this.convertValues(source["conditions"], SearchCondition);
	        this.folders = source["folders"];
	        this.dateFrom = source["dateFrom"];
	        this.dateTo = source["dateTo"];
	        this.matchMode = source["matchMode"];
	        this.offset = source["offset"];
	        this.limit = source["limit"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class AdvancedSearchResponse {
	    success: boolean;
	    items: database.SearchResult[];
	    total: number;
	    offset: number;
	    limit: number;
	    message?: string;
	
	    static createFrom(source: any = {}) {
	        return new AdvancedSearchResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.success = source["success"];
	        this.items = this.convertValues(source["items"], database.SearchResult);
	        this.total = source["total"];
	        this.offset = source["offset"];
	        this.limit = source["limit"];
	        this.message = source["message"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class DebugScanResult {
	    rootPath: string;
	    diskTotalFiles: number;
	    diskImageFiles: number;
	    appScanCount: number;
	    skippedDirs: string[];
	    failedFiles: string[];
	    sampleMissing: string[];
	
	    static createFrom(source: any = {}) {
	        return new DebugScanResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.rootPath = source["rootPath"];
	        this.diskTotalFiles = source["diskTotalFiles"];
	        this.diskImageFiles = source["diskImageFiles"];
	        this.appScanCount = source["appScanCount"];
	        this.skippedDirs = source["skippedDirs"];
	        this.failedFiles = source["failedFiles"];
	        this.sampleMissing = source["sampleMissing"];
	    }
	}
	export class FileData {
	    id: string;
	    name: string;
	    mimeType: string;
	    size: number;
	    data: number[];
	
	    static createFrom(source: any = {}) {
	        return new FileData(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.mimeType = source["mimeType"];
	        this.size = source["size"];
	        this.data = source["data"];
	    }
	}
	export class SafeImage {
	    id: string;
	    name: string;
	    path: string;
	    folder: string;
	    rootPath: string;
	    url: string;
	    thumbUrl: string;
	    size: number;
	    lastModified: number;
	    createdAt: number;
	    width: number;
	    height: number;
	    isVideo: boolean;
	
	    static createFrom(source: any = {}) {
	        return new SafeImage(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.path = source["path"];
	        this.folder = source["folder"];
	        this.rootPath = source["rootPath"];
	        this.url = source["url"];
	        this.thumbUrl = source["thumbUrl"];
	        this.size = source["size"];
	        this.lastModified = source["lastModified"];
	        this.createdAt = source["createdAt"];
	        this.width = source["width"];
	        this.height = source["height"];
	        this.isVideo = source["isVideo"];
	    }
	}
	export class FolderDiffResult {
	    added: SafeImage[];
	    removed: string[];
	    unchanged: number;
	    success: boolean;
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new FolderDiffResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.added = this.convertValues(source["added"], SafeImage);
	        this.removed = source["removed"];
	        this.unchanged = source["unchanged"];
	        this.success = source["success"];
	        this.error = source["error"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class FolderNode {
	    name: string;
	    path: string;
	    imageCount: number;
	    thumbCount: number;
	    folderType?: string;
	    children: FolderNode[];
	
	    static createFrom(source: any = {}) {
	        return new FolderNode(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.path = source["path"];
	        this.imageCount = source["imageCount"];
	        this.thumbCount = source["thumbCount"];
	        this.folderType = source["folderType"];
	        this.children = this.convertValues(source["children"], FolderNode);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ImageListResult {
	    items: SafeImage[];
	    total: number;
	    offset: number;
	    limit: number;
	
	    static createFrom(source: any = {}) {
	        return new ImageListResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.items = this.convertValues(source["items"], SafeImage);
	        this.total = source["total"];
	        this.offset = source["offset"];
	        this.limit = source["limit"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class IndexRootInfo {
	    rootPath: string;
	    total: number;
	    indexed: number;
	    done: boolean;
	    indexing: boolean;
	
	    static createFrom(source: any = {}) {
	        return new IndexRootInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.rootPath = source["rootPath"];
	        this.total = source["total"];
	        this.indexed = source["indexed"];
	        this.done = source["done"];
	        this.indexing = source["indexing"];
	    }
	}
	export class PreGenStatus {
	    running: boolean;
	    paused: boolean;
	    folder: string;
	    total: number;
	    done: number;
	    skipped: number;
	    failed: number;
	
	    static createFrom(source: any = {}) {
	        return new PreGenStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.running = source["running"];
	        this.paused = source["paused"];
	        this.folder = source["folder"];
	        this.total = source["total"];
	        this.done = source["done"];
	        this.skipped = source["skipped"];
	        this.failed = source["failed"];
	    }
	}
	export class ProxyRequestArgs {
	    id: string;
	    url: string;
	    method: string;
	    headers: Record<string, string>;
	    body: string;
	    proxyHost: string;
	    proxyPort: number;
	
	    static createFrom(source: any = {}) {
	        return new ProxyRequestArgs(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.url = source["url"];
	        this.method = source["method"];
	        this.headers = source["headers"];
	        this.body = source["body"];
	        this.proxyHost = source["proxyHost"];
	        this.proxyPort = source["proxyPort"];
	    }
	}
	
	export class ScanResult {
	    success: boolean;
	    fileCount: number;
	    folderPath?: string;
	    rootCount?: number;
	    message: string;
	    folder?: FolderNode;
	
	    static createFrom(source: any = {}) {
	        return new ScanResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.success = source["success"];
	        this.fileCount = source["fileCount"];
	        this.folderPath = source["folderPath"];
	        this.rootCount = source["rootCount"];
	        this.message = source["message"];
	        this.folder = this.convertValues(source["folder"], FolderNode);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	export class SearchResponse {
	    success: boolean;
	    items: database.SearchResult[];
	    total: number;
	    offset: number;
	    limit: number;
	    message?: string;
	
	    static createFrom(source: any = {}) {
	        return new SearchResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.success = source["success"];
	        this.items = this.convertValues(source["items"], database.SearchResult);
	        this.total = source["total"];
	        this.offset = source["offset"];
	        this.limit = source["limit"];
	        this.message = source["message"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class WindowState {
	    width: number;
	    height: number;
	    x: number;
	    y: number;
	    maximised: boolean;
	
	    static createFrom(source: any = {}) {
	        return new WindowState(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.width = source["width"];
	        this.height = source["height"];
	        this.x = source["x"];
	        this.y = source["y"];
	        this.maximised = source["maximised"];
	    }
	}

}

