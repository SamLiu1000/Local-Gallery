/* ============================================================
   wails-bridge.js - Wails 前端绑定层
    将原来通过 fetch('/api/...') 的 HTTP 调用
    替换为 Wails 的 Go 函数调用 window.go.main.App.XXX()
    
    此文件必须在所有其他 JS 文件之前加载
    ============================================================ */

const WailsBridge = (() => {
    // 检查是否在 Wails 环境中运行
    const isWailsEnv = typeof window.go !== 'undefined' && 
                       window.go.main && 
                       window.go.main.App;

    /**
     * 获取 Wails App 实例
     */
    function getApp() {
        if (!isWailsEnv) {
            throw new Error('Wails 环境不可用');
        }
        return window.go.main.App;
    }

    // ==================== 文件夹操作 ====================

    async function selectFolder() {
        if (!isWailsEnv) {
            if (window.showDirectoryPicker) {
                const handle = await window.showDirectoryPicker({ mode: 'read' });
                return handle.name;
            }
            throw new Error('文件夹选择不可用');
        }
        return getApp().SelectFolder();
    }

    async function scanFolder(path, folderType = 'ai', quick = true) {
        if (!isWailsEnv) {
            const response = await fetch('/api/scan-folder', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ path, folderType })
            });
            return response.json();
        }
        // quick=true: 快速导入（只统计数量）
        // quick=false: 完整扫描
        return getApp().ScanFolderQuick(path, folderType, quick);
    }

    async function refreshAll() {
        if (!isWailsEnv) {
            return { success: false, error: '浏览器环境不支持全量刷新' };
        }
        return getApp().RefreshAll();
    }

    async function fullRescanFolder(path) {
        if (!isWailsEnv) {
            const response = await fetch('/api/full-rescan', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ path })
            }).catch(() => null);
            if (!response) {
                throw new Error('全量重新扫描失败');
            }
            return response.json();
        }
        return getApp().FullRescanFolder(path);
    }

    async function rescanFolder(path) {
        if (!isWailsEnv) {
            const response = await fetch('/api/refresh', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ path })
            }).catch(() => null);
            if (!response) {
                throw new Error('重新扫描失败');
            }
            return response.json();
        }
        return getApp().RescanFolder(path);
    }

    async function refreshFolder(folderPath) {
        if (!isWailsEnv) {
            return { success: false, error: '浏览器环境不支持增量刷新' };
        }
        return getApp().RefreshFolder(folderPath);
    }

    async function removeFolder(path) {
        if (!isWailsEnv) {
            const response = await fetch('/api/remove-folder', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ path })
            });
            return response.json();
        }
        return getApp().RemoveFolder(path);
    }

    async function refresh() {
        if (!isWailsEnv) {
            const response = await fetch('/api/refresh', { method: 'POST' });
            return response.json();
        }
        return getApp().Refresh();
    }

    async function removeImages(ids) {
        if (!isWailsEnv) {
            const response = await fetch('/api/images/remove', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ ids })
            });
            return response.json();
        }
        return getApp().RemoveImages(ids);
    }

    // ==================== 图片数据 ====================

    async function getImages(params = {}) {
        const { folder = '', offset = 0, limit = 0, sortOrder = '' } = params;
        if (!isWailsEnv) {
            let url = '/api/images?';
            if (folder) url += 'folder=' + encodeURIComponent(folder) + '&';
            if (offset) url += 'offset=' + offset + '&';
            if (limit) url += 'limit=' + limit + '&';
            if (sortOrder) url += 'sortOrder=' + encodeURIComponent(sortOrder);
            const response = await fetch(url);
            return response.json();
        }
        return getApp().GetImages(folder, offset, limit, sortOrder);
    }

    async function getImagesByPaths(paths, offset = 0, limit = 0, sortOrder = '') {
        if (!isWailsEnv) {
            const response = await fetch('/api/images-by-paths', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ paths, offset, limit, sortOrder })
            });
            return response.json();
        }
        return getApp().GetImagesByPaths(paths, offset, limit, sortOrder);
    }

    async function getFolders() {
        if (!isWailsEnv) {
            const response = await fetch('/api/folders');
            return response.json();
        }
        return getApp().GetFolders();
    }

    async function getFolderCount(folderPath) {
        if (!isWailsEnv) {
            return -1;
        }
        return getApp().GetFolderCount(folderPath);
    }

    async function getImageFile(imageID) {
        if (!isWailsEnv) {
            const response = await fetch('/image/' + imageID);
            const blob = await response.blob();
            return new Promise((resolve, reject) => {
                const reader = new FileReader();
                reader.onload = () => {
                    const base64 = reader.result.split(',')[1];
                    resolve({
                        id: imageID,
                        name: '',
                        mimeType: blob.type,
                        size: blob.size,
                        data: base64
                    });
                };
                reader.onerror = reject;
                reader.readAsDataURL(blob);
            });
        }
        return getApp().GetImageFile(imageID);
    }

    async function getThumbnail(imageID) {
        if (!isWailsEnv) {
            const response = await fetch('/thumb/' + imageID);
            const blob = await response.blob();
            return new Promise((resolve, reject) => {
                const reader = new FileReader();
                reader.onload = () => {
                    const base64 = reader.result.split(',')[1];
                    resolve({
                        id: imageID,
                        name: '',
                        mimeType: blob.type,
                        size: blob.size,
                        data: base64
                    });
                };
                reader.onerror = reject;
                reader.readAsDataURL(blob);
            });
        }
        return getApp().GetThumbnail(imageID);
    }

    async function openFileLocation(imageID) {
        if (!isWailsEnv) {
            const response = await fetch('/api/open-file', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ id: imageID })
            });
            return response.json();
        }
        return getApp().OpenFileLocation(imageID);
    }

    // ==================== 反推日志 ====================

    async function appendReverseLog(name, path, errMsg) {
        if (!isWailsEnv) return;
        return getApp().AppendReverseLog(name, path, errMsg);
    }

    async function getReverseLog() {
        if (!isWailsEnv) return '';
        return getApp().GetReverseLog();
    }

    async function openReverseLog() {
        if (!isWailsEnv) return;
        return getApp().OpenReverseLog();
    }

    // ==================== 用户数据 ====================

    async function getAllUserData() {
        if (!isWailsEnv) {
            const response = await fetch('/api/user-data/get-all');
            return response.json();
        }
        return getApp().GetAllUserData();
    }

    async function saveUserData(data) {
        if (!isWailsEnv) {
            const response = await fetch('/api/user-data/save', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ data })
            });
            return response.json();
        }
        return getApp().SaveUserData(data);
    }

    async function saveRoots(roots) {
        if (!isWailsEnv) {
            const response = await fetch('/api/user-data/save-roots', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ roots })
            });
            return response.json();
        }
        return getApp().SaveRoots(roots);
    }

    // ==================== 用户数据（SQLite） ====================

    async function getImportedRoots() {
        if (!isWailsEnv) {
            const response = await fetch('/api/roots');
            return response.json();
        }
        return getApp().GetImportedRoots();
    }

    async function saveRootsWithMeta(roots) {
        return getApp().SaveRootsWithMeta(roots);
    }

    async function getSidebarSetting(key) {
        return getApp().GetSidebarSetting(key);
    }

    async function setSidebarSetting(key, value) {
        return getApp().SetSidebarSetting(key, value);
    }

    async function getAllSidebarSettings() {
        return getApp().GetAllSidebarSettings();
    }

    async function addImageTag(imagePath, tagId) {
        return getApp().AddImageTag(imagePath, tagId);
    }

    async function removeImageTag(imagePath, tagId) {
        return getApp().RemoveImageTag(imagePath, tagId);
    }

    async function removeImageTagsByTagId(tagId) {
        return getApp().RemoveImageTagsByTagID(tagId);
    }

    async function getImageTags(imagePath) {
        return getApp().GetImageTags(imagePath);
    }

    async function getImagesForTagIds(tagIds) {
        return getApp().GetImagesForTagIds(tagIds);
    }

    async function getAllImageTags() {
        return getApp().GetAllImageTags();
    }

    async function importImageTags(tags) {
        return getApp().ImportImageTags(tags);
    }

    async function toggleFavorite(imagePath) {
        return getApp().ToggleFavorite(imagePath);
    }

    async function setFavorite(imagePath, value) {
        return getApp().SetFavorite(imagePath, value);
    }

    async function getAllFavorites() {
        return getApp().GetAllFavorites();
    }

    async function isFavorite(imagePath) {
        return getApp().IsFavorite(imagePath);
    }

    // ==================== 代理 ====================

    async function proxyRequest(req) {
        if (!isWailsEnv) {
            const response = await fetch('/proxy', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(req)
            });
            return response.json();
        }
        // Wails 环境：Go 的 ProxyRequest 立即返回 {success:true}，
        // 真正的 HTTP 响应通过 proxy:result:<id> 事件异步发回
        return new Promise((resolve, reject) => {
            const requestId = req.requestId || req.ID || req.id;
            if (!requestId) {
                reject(new Error('缺少 requestId'));
                return;
            }
            // 先调用 Go 发起请求（Go 端用 "id" 字段，前端用 "requestId"）
            getApp().ProxyRequest({ ...req, id: requestId });
            // 监听事件获取结果
            const eventName = 'proxy:result:' + requestId;
            const handler = (result) => {
                window.runtime.EventsOff(eventName);
                if (result.error) {
                    reject(new Error(result.error));
                } else {
                    resolve({
                        statusCode: result.status || result.statusCode || 0,
                        body: result.body || '',
                        headers: result.headers || {}
                    });
                }
            };
            window.runtime.EventsOn(eventName, handler);
            // 超时保护（120秒）
            setTimeout(() => {
                window.runtime.EventsOff(eventName);
                reject(new Error('代理请求超时'));
            }, 120000);
        });
    }

    function cancelProxyRequest(requestId) {
        if (!isWailsEnv || !requestId) return;
        getApp().CancelProxyRequest(requestId);
    }

    // ==================== 元数据 ====================

    async function parseMetadata(filePath) {
        if (!isWailsEnv) {
            throw new Error('浏览器环境不支持直接解析文件元数据');
        }
        return getApp().ParseMetadata(filePath);
    }

    // getHTTPBaseURL 的同步缓存
    let _httpBaseURL = '';

    async function getHTTPBaseURL() {
        if (!isWailsEnv) {
            return '';
        }
        const url = await getApp().GetHTTPBaseURL();
        _httpBaseURL = url || '';
        return _httpBaseURL;
    }

    async function getThumbBaseURL2() {
        if (!isWailsEnv) {
            return '';
        }
        return getApp().GetThumbBaseURL2();
    }

    // getImageBaseURL 的同步缓存
    let _imageBaseURL = '';

    async function getImageBaseURL() {
        if (!isWailsEnv) {
            return '';
        }
        const url = await getApp().GetImageBaseURL();
        _imageBaseURL = url || '';
        return _imageBaseURL;
    }

    // fixRelativeUrls 把 HTML/CSS 中的 /image/ 和 /thumb/ 相对路径替换为完整 URL
    // 处理: CSS url(), <img src>, <image href>, src 属性中的相对路径
    // 因为原图/缩略图服务是独立端口，相对路径无法被浏览器正确解析
    function fixRelativeUrls(htmlOrCssText) {
        if (!htmlOrCssText) return htmlOrCssText;
        let result = htmlOrCssText;
        if (_imageBaseURL) {
            // CSS url(/image/...) 和 url("/image/...") 和 url('/image/...')
            result = result.replace(/url\((['"]?)\/image\//g, 'url($1' + _imageBaseURL + '/image/');
            // <img src="/image/..."> 和 src="/image/..."
            result = result.replace(/(src|href)=(['"])\/image\//g, '$1=$2' + _imageBaseURL + '/image/');
        }
        if (_httpBaseURL) {
            // CSS url(/thumb/...)
            result = result.replace(/url\((['"]?)\/thumb\//g, 'url($1' + _httpBaseURL + '/thumb/');
            // <img src="/thumb/..."> 等
            result = result.replace(/(src|href)=(['"])\/thumb\//g, '$1=$2' + _httpBaseURL + '/thumb/');
        }
        return result;
    }

    // ==================== 搜索 ====================

    async function searchImages(query, folder, offset, limit) {
        if (!isWailsEnv) {
            const response = await fetch('/api/search', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ query, folder, offset, limit })
            });
            return response.json();
        }
        return getApp().SearchImages(query, folder || '', offset || 0, limit || 50);
    }

    async function advancedSearch(params) {
        if (!isWailsEnv) {
            const response = await fetch('/api/search/advanced', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(params)
            });
            return response.json();
        }
        console.log('[WailsBridge] advancedSearch 调用 Go, params:', JSON.stringify(params));
        try {
            const result = await getApp().AdvancedSearch(params);
            console.log('[WailsBridge] advancedSearch Go 返回:', JSON.stringify({ success: result?.success, total: result?.total, message: result?.message }));
            return result;
        } catch (err) {
            console.error('[WailsBridge] advancedSearch 调用失败:', err);
            return { success: false, message: err.message || String(err) };
        }
    }

    // ==================== Prompt 版本 ====================

    async function addPromptVersion(imagePath, positivePrompt, negativePrompt, source) {
        if (!isWailsEnv) {
            throw new Error('非Wails环境不支持直接添加Prompt版本');
        }
        return getApp().AddPromptVersion(imagePath, positivePrompt, negativePrompt, source);
    }

    async function getPromptVersions(imagePath) {
        if (!isWailsEnv) {
            throw new Error('非Wails环境不支持直接获取Prompt版本');
        }
        return getApp().GetPromptVersions(imagePath);
    }

    async function deletePromptVersion(id) {
        if (!isWailsEnv) {
            throw new Error('非Wails环境不支持直接删除Prompt版本');
        }
        return getApp().DeletePromptVersion(id);
    }

    async function getAllPromptVersions() {
        if (!isWailsEnv) {
            throw new Error('非Wails环境不支持直接获取所有Prompt版本');
        }
        return getApp().GetAllPromptVersions();
    }

    async function getAllPromptVersionCounts() {
        if (!isWailsEnv) {
            throw new Error('非Wails环境不支持直接获取Prompt版本计数');
        }
        return getApp().GetAllPromptVersionCounts();
    }

    async function importPromptVersions(versions) {
        if (!isWailsEnv) {
            throw new Error('非Wails环境不支持直接导入Prompt版本');
        }
        return getApp().ImportPromptVersions(versions);
    }

    // ==================== 缩略图目录设置 ====================

    async function getThumbDir() {
        if (!isWailsEnv) return '';
        return getApp().GetThumbDir();
    }

    async function setThumbDir(path) {
        if (!isWailsEnv) return { success: false, error: '非Wails环境' };
        return getApp().SetThumbDir(path);
    }

    async function getThumbCacheInfo() {
        if (!isWailsEnv) return { count: 0, totalSize: 0, totalSizeStr: '0 B', dir: '' };
        return getApp().GetThumbCacheInfo();
    }

    async function cleanOrphanedThumbs() {
        if (!isWailsEnv) return { success: false, error: '非Wails环境' };
        return getApp().CleanOrphanedThumbs();
    }

    async function clearFolderThumbs(folderPath) {
        if (!isWailsEnv) return { success: false, error: '非Wails环境' };
        return getApp().ClearFolderThumbs(folderPath);
    }

    async function startPreGenThumbs(folders) {
        if (!isWailsEnv) return { success: false, message: '非Wails环境' };
        return getApp().StartPreGenThumbs(folders);
    }

    async function stopPreGenThumbs() {
        if (!isWailsEnv) return { success: false };
        return getApp().StopPreGenThumbs();
    }

    async function pausePreGenThumbs() {
        if (!isWailsEnv) return { success: false };
        return getApp().PausePreGenThumbs();
    }

    async function resumePreGenThumbs() {
        if (!isWailsEnv) return { success: false };
        return getApp().ResumePreGenThumbs();
    }

    async function getPreGenStatus() {
        if (!isWailsEnv) return { running: false, paused: false, folder: '', total: 0, done: 0, skipped: 0, failed: 0 };
        return getApp().GetPreGenStatus();
    }

    async function getThumbGeneration() {
        if (!isWailsEnv) return 0;
        return getApp().GetThumbGeneration();
    }

    async function getThumbConcurrency() {
        if (!isWailsEnv) return 2;
        return getApp().GetThumbConcurrency();
    }

    async function setThumbConcurrency(n) {
        if (!isWailsEnv) return { success: false, error: '非Wails环境' };
        return getApp().SetThumbConcurrency(n);
    }

    async function getThumbKernel() {
        if (!isWailsEnv) return 'lanczos3';
        return getApp().GetThumbKernel();
    }

    async function setThumbKernel(kernel) {
        if (!isWailsEnv) return { success: false, error: '非Wails环境' };
        return getApp().SetThumbKernel(kernel);
    }

    // ==================== 用户数据目录设置 ====================

    async function getUserDataDir() {
        if (!isWailsEnv) return '';
        return getApp().GetUserDataDir();
    }

    async function setUserDataDir(path) {
        if (!isWailsEnv) return { success: false, error: '非Wails环境' };
        return getApp().SetUserDataDir(path);
    }

    async function pauseBackground() {
        if (!isWailsEnv) return;
        return getApp().PauseBackground();
    }

    async function resumeBackground() {
        if (!isWailsEnv) return;
        return getApp().ResumeBackground();
    }

    async function restartWithNewPaths() {
        if (!isWailsEnv) return { success: false, error: '非Wails环境' };
        return getApp().RestartWithNewPaths();
    }

    async function saveFile(defaultName, content) {
        if (!isWailsEnv) {
            throw new Error('非Wails环境不支持原生保存对话框');
        }
        return getApp().SaveFile(defaultName, content);
    }

    function startFileDrag(filePath) {
        if (!isWailsEnv || !filePath) return;
        getApp().StartFileDrag(filePath);
    }

    // ==================== 图标文件选择 ====================

    async function pickIconFile() {
        if (!isWailsEnv) {
            throw new Error('非Wails环境不支持原生文件对话框');
        }
        return getApp().PickIconFile();
    }

    // SaveAvatar 保存 base64 JPEG 为头像文件，返回相对路径如 /avatar/xxx.jpg
    async function saveAvatar(base64Data) {
        if (!isWailsEnv) {
            throw new Error('非Wails环境不支持保存头像文件');
        }
        return getApp().SaveAvatar(base64Data);
    }

    // getAvatarUrl 返回头像文件的完整 HTTP URL
    function getAvatarUrl(avatarPath) {
        if (!avatarPath) return '';
        if (avatarPath.startsWith('data:') || avatarPath.startsWith('blob:')) return avatarPath;
        if (avatarPath.startsWith('http')) return avatarPath;
        return (_httpBaseURL || '') + avatarPath;
    }

    // ==================== 工具方法 ====================

    function base64ToBlobUrl(fileData) {
        const byteCharacters = atob(fileData.data);
        const byteNumbers = new Array(byteCharacters.length);
        for (let i = 0; i < byteCharacters.length; i++) {
            byteNumbers[i] = byteCharacters.charCodeAt(i);
        }
        const byteArray = new Uint8Array(byteNumbers);
        const blob = new Blob([byteArray], { type: fileData.mimeType });
        return URL.createObjectURL(blob);
    }

    function isWails() {
        return isWailsEnv;
    }

    // ==================== 搜索索引 ====================

    async function getFolderIndexStatus() {
        if (!isWailsEnv) {
            const response = await fetch('/api/folder-index-status');
            if (!response.ok) return [];
            return response.json();
        }
        return getApp().GetFolderIndexStatus();
    }

    async function indexRoot(rootPath) {
        if (!isWailsEnv) {
            await fetch('/api/index-root', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ rootPath })
            });
            return;
        }
        getApp().IndexRoot(rootPath);
    }

    async function stopIndexRoot(rootPath) {
        if (!isWailsEnv) {
            await fetch('/api/stop-index-root', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ rootPath })
            });
            return;
        }
        getApp().StopIndexRoot(rootPath);
    }

    // ==================== 公开 API ====================

    // 预热缓存，避免 HTML 标签首次渲染时 URL 未初始化
    if (isWailsEnv) {
        getApp().GetHTTPBaseURL().then(url => { _httpBaseURL = url || ''; }).catch(() => {});
        getApp().GetImageBaseURL().then(url => { _imageBaseURL = url || ''; }).catch(() => {});
    }

    return {
        isWails,
        selectFolder,
        scanFolder,
        refreshAll,
        rescanFolder,
        fullRescanFolder,
        removeFolder,
        removeImages,
        refresh,
        refreshFolder,
        getImages,
        getImagesByPaths,
        getFolders,
        getFolderCount,
        getImageFile,
        getThumbnail,
        openFileLocation,
        appendReverseLog,
        getReverseLog,
        openReverseLog,
        getAllUserData,
        saveUserData,
        saveRoots,
        getImportedRoots,
        saveRootsWithMeta,
        getSidebarSetting,
        setSidebarSetting,
        getAllSidebarSettings,
        addImageTag,
        removeImageTag,
        removeImageTagsByTagId,
        getImageTags,
        getImagesForTagIds,
        getAllImageTags,
        importImageTags,
        toggleFavorite,
        setFavorite,
        getAllFavorites,
        isFavorite,
        proxyRequest,
        cancelProxyRequest,
        parseMetadata,
        getHTTPBaseURL,
        getThumbBaseURL2,
        getImageBaseURL,
        fixRelativeUrls,
        searchImages,
        advancedSearch,
        addPromptVersion,
        getPromptVersions,
        deletePromptVersion,
        getAllPromptVersions,
        getAllPromptVersionCounts,
        importPromptVersions,
        getThumbDir,
        setThumbDir,
        getThumbCacheInfo,
        cleanOrphanedThumbs,
        clearFolderThumbs,
        startPreGenThumbs,
        stopPreGenThumbs,
        pausePreGenThumbs,
        resumePreGenThumbs,
        getPreGenStatus,
        getThumbGeneration,
        getThumbConcurrency,
        setThumbConcurrency,
        getThumbKernel,
        setThumbKernel,
        getUserDataDir,
        setUserDataDir,
        pauseBackground,
        resumeBackground,
        restartWithNewPaths,
        saveFile,
        startFileDrag,
        pickIconFile,
        saveAvatar,
        getAvatarUrl,
        base64ToBlobUrl,
        getFolderIndexStatus,
        indexRoot,
        stopIndexRoot
    };
})();