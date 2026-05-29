/* ============================================================
   storage.js - 持久化存储层（v5 - Wails 适配）
    管理：标签、收藏、API配置、提示词版本、图片-标签关联、FileSystemDirectoryHandle
    Wails 环境：通过 WailsBridge 调用 Go 后端
    浏览器环境：通过 fetch 调用 HTTP API，回退到 IndexedDB
   ============================================================ */

const Storage = (() => {
    const DB_NAME = 'LocalGalleryDB';
    const DB_VERSION = 2;

    /** @type {IDBDatabase|null} */
    let db = null;

    /** 后端 API 基础路径 */
    const API_BASE = '/api/user-data';

    /** 是否检测到后端服务器 */
    let serverAvailable = false;

    /** 自动保存定时器 */
    let autoSaveTimer = null;

    /** 防抖延迟（毫秒） */
    const SAVE_DEBOUNCE_MS = 300;

    /** 是否有未保存的更改 */
    let hasUnsavedChanges = false;

    // ==================== 初始化 ====================

    async function init() {
        // 先检测后端是否可用
        serverAvailable = await checkServerAvailable();

        if (serverAvailable) {
            console.log('[Storage] 使用后端文件系统存储（user/ 目录）');
            try {
                await syncFromServer();
                console.log('[Storage] 从后端加载用户数据成功');
            } catch (err) {
                console.warn('[Storage] 从后端加载数据失败:', err.message);
            }
            
            // 启动自动保存
            startAutoSave();
            
            return;
        }

        // 回退到 IndexedDB
        console.log('[Storage] 后端不可用，回退到 IndexedDB');
        return initIndexedDB();
    }

    function scheduleDebouncedSave() {
        if (autoSaveTimer) {
            clearTimeout(autoSaveTimer);
        }
        autoSaveTimer = setTimeout(() => {
            autoSaveTimer = null;
            if (hasUnsavedChanges && serverAvailable) {
                saveToServer().then(() => {
                    hasUnsavedChanges = false;
                }).catch(err => {
                    console.warn('[Storage] 自动保存失败:', err.message);
                    hasUnsavedChanges = true;
                });
            }
        }, SAVE_DEBOUNCE_MS);
    }

    function startAutoSave() {
        // 兼容旧调用，首次启动时不做任何事（markDirty 会触发防抖）
    }

    // 页面关闭前保存
    window.addEventListener('beforeunload', () => {
        if (hasUnsavedChanges && serverAvailable) {
            const data = JSON.stringify({
                data: {
                    tags: serverDataCache.tags,
                    apiConfigs: serverDataCache.apiConfigs,
                    settings: serverDataCache.settings,
                    favorites: serverDataCache.favorites,
                    imageTags: serverDataCache.imageTags
                }
            });
            if (typeof WailsBridge !== 'undefined' && WailsBridge.isWails()) {
                WailsBridge.saveUserData(JSON.parse(data).data).catch(() => {});
            } else {
                navigator.sendBeacon(API_BASE + '/save', new Blob([data], { type: 'application/json' }));
            }
        }
    });

    function markDirty() {
        hasUnsavedChanges = true;
        scheduleDebouncedSave();
    }

    function initIndexedDB() {
        return new Promise((resolve, reject) => {
            const request = indexedDB.open(DB_NAME, DB_VERSION);

            request.onupgradeneeded = (e) => {
                const database = e.target.result;

                if (!database.objectStoreNames.contains('tags')) {
                    const tagStore = database.createObjectStore('tags', { keyPath: 'id' });
                    tagStore.createIndex('parentId', 'parentId', { unique: false });
                    tagStore.createIndex('name', 'name', { unique: false });
                }

                if (!database.objectStoreNames.contains('imageTags')) {
                    const imageTagStore = database.createObjectStore('imageTags', { keyPath: 'id', autoIncrement: true });
                    imageTagStore.createIndex('imagePath', 'imagePath', { unique: false });
                    imageTagStore.createIndex('tagId', 'tagId', { unique: false });
                    imageTagStore.createIndex('imagePath_tagId', ['imagePath', 'tagId'], { unique: true });
                }

                if (!database.objectStoreNames.contains('favorites')) {
                    database.createObjectStore('favorites', { keyPath: 'imagePath' });
                }

                if (!database.objectStoreNames.contains('apiConfigs')) {
                    const apiStore = database.createObjectStore('apiConfigs', { keyPath: 'id' });
                    apiStore.createIndex('isDefault', 'isDefault', { unique: false });
                }

                if (!database.objectStoreNames.contains('promptVersions')) {
                    const promptStore = database.createObjectStore('promptVersions', { keyPath: 'id', autoIncrement: true });
                    promptStore.createIndex('imagePath', 'imagePath', { unique: false });
                }

                if (!database.objectStoreNames.contains('settings')) {
                    database.createObjectStore('settings', { keyPath: 'key' });
                }

                if (!database.objectStoreNames.contains('directoryHandles')) {
                    const dhStore = database.createObjectStore('directoryHandles', { keyPath: 'id' });
                    dhStore.createIndex('name', 'name', { unique: false });
                }
            };

            request.onsuccess = (e) => {
                db = e.target.result;
                console.log('[Storage] IndexedDB 初始化成功');
                resolve(db);
            };

            request.onerror = (e) => {
                console.error('[Storage] IndexedDB 初始化失败:', e.target.error);
                reject(e.target.error);
            };

            request.onblocked = () => {
                console.warn('[Storage] IndexedDB 初始化被阻止');
                reject(new Error('IndexedDB 被其他标签页占用'));
            };
        });
    }

    // ==================== 后端检测 ====================

    async function checkServerAvailable() {
        // ★ Wails 环境下始终可用
        if (typeof WailsBridge !== 'undefined' && WailsBridge.isWails()) {
            return true;
        }
        try {
            const controller = new AbortController();
            const timeoutId = setTimeout(() => controller.abort(), 3000);
            const response = await fetch(API_BASE + '/get-all', {
                method: 'GET',
                signal: controller.signal
            });
            clearTimeout(timeoutId);
            return response.ok;
        } catch {
            return false;
        }
    }

    // ==================== 数据同步 ====================

    let serverDataCache = {
        tags: [],
        apiConfigs: [],
        settings: {}
    };

    async function syncFromServer() {
        if (!serverAvailable) return;
        try {
            let result;
            // ★ Wails 环境：直接调用 Go 函数
            if (typeof WailsBridge !== 'undefined' && WailsBridge.isWails()) {
                result = await WailsBridge.getAllUserData();
            } else {
                const response = await fetch(API_BASE + '/get-all');
                if (!response.ok) throw new Error('获取用户数据失败 (HTTP ' + response.status + ')');
                result = await response.json();
            }
            if (result.success && result.data) {
                serverDataCache = {
                    tags: result.data.tags || [],
                    apiConfigs: result.data.apiConfigs || [],
                    settings: result.data.settings || {},
                    favorites: result.data.favorites || [],
                    imageTags: result.data.imageTags || []
                };
                console.log('[Storage] 从后端同步数据成功:',
                    '标签:', serverDataCache.tags.length,
                    'API配置:', serverDataCache.apiConfigs.length,
                    '设置字段:', Object.keys(serverDataCache.settings).length
                );
            } else {
                throw new Error('后端返回数据格式错误');
            }
        } catch (err) {
            console.warn('[Storage] 从后端同步数据失败:', err.message);
            throw err;
        }
    }

    async function saveToServer() {
        if (!serverAvailable) return;
        try {
            const dataToSave = {
                tags: serverDataCache.tags,
                apiConfigs: serverDataCache.apiConfigs,
                settings: serverDataCache.settings,
                favorites: serverDataCache.favorites,
                imageTags: serverDataCache.imageTags
            };

            let result;
            // ★ Wails 环境：直接调用 Go 函数
            if (typeof WailsBridge !== 'undefined' && WailsBridge.isWails()) {
                result = await WailsBridge.saveUserData(dataToSave);
            } else {
                const response = await fetch(API_BASE + '/save', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ data: dataToSave })
                });
                
                if (!response.ok) {
                    const errorText = await response.text().catch(() => '未知错误');
                    throw new Error(`保存失败 (HTTP ${response.status}): ${errorText}`);
                }
                
                result = await response.json();
            }
            
            if (!result.success) {
                throw new Error('后端返回保存失败: ' + (result.error || '未知错误'));
            }
            
            hasUnsavedChanges = false;
            console.log('[Storage] 数据已保存到后端');
        } catch (err) {
            console.warn('[Storage] 保存数据到后端失败:', err.message);
            throw err;
        }
    }

    // ==================== 通用操作 ====================

    function promisify(store, method, ...args) {
        return new Promise((resolve, reject) => {
            const request = store[method](...args);
            request.onsuccess = () => resolve(request.result);
            request.onerror = () => reject(request.error);
        });
    }

    function getStore(storeName, mode = 'readonly') {
        if (!db) throw new Error('数据库未初始化');
        const tx = db.transaction(storeName, mode);
        return tx.objectStore(storeName);
    }

    // ==================== FileSystemDirectoryHandle 操作 ====================

    async function saveDirectoryHandle(id, name, handle) {
        const store = getStore('directoryHandles', 'readwrite');
        await promisify(store, 'put', { id, name, handle, savedAt: new Date().toISOString() });
    }

    async function getAllDirectoryHandles() {
        const store = getStore('directoryHandles');
        return await promisify(store, 'getAll');
    }

    async function removeDirectoryHandle(id) {
        const store = getStore('directoryHandles', 'readwrite');
        await promisify(store, 'delete', id);
    }

    async function clearDirectoryHandles() {
        const store = getStore('directoryHandles', 'readwrite');
        await promisify(store, 'clear');
    }

    // ==================== 扫描目录 roots 操作 ====================

    async function getRegisteredRoots() {
        if (serverAvailable) {
            try {
                if (typeof WailsBridge !== 'undefined' && WailsBridge.isWails()) {
                    const roots = await WailsBridge.getImportedRoots();
                    return (roots || []).map(r => r.path || r.Path);
                } else {
                    const response = await fetch(API_BASE + '/get-all');
                    if (!response.ok) throw new Error('HTTP ' + response.status);
                    const result = await response.json();
                    if (result && result.success && result.data && result.data.registeredRoots) {
                        return result.data.registeredRoots;
                    }
                }
            } catch (err) {
                console.warn('[Storage] 获取 registeredRoots 失败:', err.message);
            }
            return [];
        }
        return [];
    }

    async function setRegisteredRoots(roots) {
        const normalizedRoots = Array.isArray(roots)
            ? roots.filter(item => typeof item === 'string' && item.trim()).map(item => item.trim())
            : [];

        if (serverAvailable) {
            if (typeof WailsBridge !== 'undefined' && WailsBridge.isWails()) {
                const importedRoots = normalizedRoots.map(p => ({
                    path: p,
                    name: p.split(/[\\/]/).pop(),
                    displayName: '',
                    folderType: '',
                    handleName: '',
                    addedAt: new Date().toISOString()
                }));
                const result = await WailsBridge.saveRootsWithMeta(importedRoots);
                if (!result.success) {
                    throw new Error(result.error || '保存扫描目录失败');
                }
            } else {
                const response = await fetch(API_BASE + '/save-roots', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ roots: normalizedRoots })
                });

                if (!response.ok) {
                    const result = await response.json().catch(() => ({}));
                    throw new Error(result.error || '保存扫描目录失败');
                }
            }

            return normalizedRoots;
        }

        return normalizedRoots;
    }

    // ==================== 标签操作 ====================

    async function getAllTags() {
        if (serverAvailable) {
            return serverDataCache.tags || [];
        }
        const store = getStore('tags');
        return await promisify(store, 'getAll');
    }

    async function addTag(tag) {
        if (serverAvailable) {
            tag.id = tag.id || 'tag_' + Date.now() + '_' + Math.random().toString(36).substr(2, 6);
            tag.createdAt = tag.createdAt || new Date().toISOString();
            serverDataCache.tags.push(tag);
            markDirty();
            return tag;
        }
        const store = getStore('tags', 'readwrite');
        tag.id = tag.id || 'tag_' + Date.now() + '_' + Math.random().toString(36).substr(2, 6);
        tag.createdAt = tag.createdAt || new Date().toISOString();
        await promisify(store, 'add', tag);
        return tag;
    }

    async function updateTag(tag) {
        if (serverAvailable) {
            const idx = serverDataCache.tags.findIndex(t => t.id === tag.id);
            if (idx !== -1) serverDataCache.tags[idx] = tag;
            markDirty();
            return tag;
        }
        const store = getStore('tags', 'readwrite');
        await promisify(store, 'put', tag);
        return tag;
    }

    async function deleteTag(tagId) {
        if (serverAvailable) {
            for (const t of serverDataCache.tags) {
                if (t.parentId === tagId) {
                    t.parentId = null;
                }
            }
            serverDataCache.tags = serverDataCache.tags.filter(t => t.id !== tagId);
            // 清理 SQLite 中的标签关联
            if (typeof WailsBridge !== 'undefined' && WailsBridge.isWails()) {
                await WailsBridge.removeImageTagsByTagId(tagId);
            }
            markDirty();
            return;
        }
        // IndexedDB: 孤儿化子标签
        const store = getStore('tags', 'readwrite');
        const allTags = await promisify(store, 'getAll');
        for (const t of allTags) {
            if (t.parentId === tagId) {
                t.parentId = null;
                await promisify(store, 'put', t);
            }
        }
        await promisify(store, 'delete', tagId);
        const itStore = getStore('imageTags', 'readwrite');
        const all = await promisify(itStore, 'getAll');
        for (const item of all) {
            if (item.tagId === tagId) {
                await promisify(itStore, 'delete', item.id);
            }
        }
    }

    /** 获取指定父标签的直接子标签 */
    function getChildTags(parentId) {
        const all = serverAvailable ? (serverDataCache.tags || []) : [];
        return all.filter(t => t.parentId === parentId);
    }

    // ==================== 图片-标签关联 ====================

    async function getTagsForImage(imagePath) {
        if (typeof WailsBridge !== 'undefined' && WailsBridge.isWails()) {
            const imgTags = await WailsBridge.getImageTags(imagePath);
            const tagIds = (imgTags || []).map(it => it.tagId || it.TagID);
            const allTags = serverDataCache.tags || [];
            return allTags.filter(t => tagIds.includes(t.id));
        }
        if (serverAvailable) {
            const imgTags = serverDataCache.imageTags || [];
            const tagIds = imgTags.filter(it => it.imagePath === imagePath).map(it => it.tagId);
            const allTags = serverDataCache.tags || [];
            return allTags.filter(t => tagIds.includes(t.id));
        }
        const store = getStore('imageTags');
        const all = await promisify(store, 'getAll');
        const tagIds = all.filter(it => it.imagePath === imagePath).map(it => it.tagId);
        const tagStore = getStore('tags');
        const allTags = await promisify(tagStore, 'getAll');
        return allTags.filter(t => tagIds.includes(t.id));
    }

    async function addTagToImage(imagePath, tagId) {
        if (typeof WailsBridge !== 'undefined' && WailsBridge.isWails()) {
            await WailsBridge.addImageTag(imagePath, tagId);
            return;
        }
        if (serverAvailable) {
            const imgTags = serverDataCache.imageTags || [];
            if (imgTags.some(it => it.imagePath === imagePath && it.tagId === tagId)) {
                return;
            }
            imgTags.push({ imagePath, tagId, addedAt: new Date().toISOString() });
            serverDataCache.imageTags = imgTags;
            markDirty();
            return;
        }
        const store = getStore('imageTags', 'readwrite');
        const all = await promisify(store, 'getAll');
        if (all.some(it => it.imagePath === imagePath && it.tagId === tagId)) {
            return;
        }
        await promisify(store, 'add', { imagePath, tagId, addedAt: new Date().toISOString() });
    }

    async function removeTagFromImage(imagePath, tagId) {
        if (typeof WailsBridge !== 'undefined' && WailsBridge.isWails()) {
            await WailsBridge.removeImageTag(imagePath, tagId);
            return;
        }
        if (serverAvailable) {
            const imgTags = serverDataCache.imageTags || [];
            serverDataCache.imageTags = imgTags.filter(it => !(it.imagePath === imagePath && it.tagId === tagId));
            markDirty();
            return;
        }
        const store = getStore('imageTags', 'readwrite');
        const all = await promisify(store, 'getAll');
        for (const item of all) {
            if (item.imagePath === imagePath && item.tagId === tagId) {
                await promisify(store, 'delete', item.id);
                break;
            }
        }
    }

    async function getTagDescendantIds(tagId) {
        const tags = serverAvailable
            ? (serverDataCache.tags || [])
            : await (async () => { const s = getStore('tags'); return await promisify(s, 'getAll'); })();
        const ids = [];
        const stack = [tagId];
        while (stack.length) {
            const pid = stack.pop();
            for (const t of tags) {
                if (t.parentId === pid) {
                    ids.push(t.id);
                    stack.push(t.id);
                }
            }
        }
        return ids;
    }

    async function getImagesForTag(tagId) {
        // ★ 文件夹标签：返回特殊标记，由 Gallery 直接按路径查图
        const allTags = serverAvailable ? (serverDataCache.tags || []) : [];
        const tag = allTags.find(t => t.id === tagId);
        if (tag && tag.linkedFolder) {
            return { linkedFolder: tag.linkedFolder };
        }

        // 收集标签及其所有子标签的 ID
        const descendantIds = await getTagDescendantIds(tagId);
        const allTagIds = [tagId, ...descendantIds];
        const tagIdSet = new Set(allTagIds);

        if (typeof WailsBridge !== 'undefined' && WailsBridge.isWails()) {
            const paths = await WailsBridge.getImagesForTagIds(allTagIds);
            return paths || [];
        }
        if (serverAvailable) {
            const imgTags = serverDataCache.imageTags || [];
            return imgTags.filter(it => tagIdSet.has(it.tagId)).map(it => it.imagePath);
        }
        const store = getStore('imageTags');
        const all = await promisify(store, 'getAll');
        return all.filter(it => tagIdSet.has(it.tagId)).map(it => it.imagePath);
    }

    // ==================== 收藏操作 ====================

    async function isFavorite(imagePath) {
        if (typeof WailsBridge !== 'undefined' && WailsBridge.isWails()) {
            return await WailsBridge.isFavorite(imagePath);
        }
        if (serverAvailable) {
            const favs = serverDataCache.favorites || [];
            return favs.includes(imagePath);
        }
        const store = getStore('favorites');
        const result = await promisify(store, 'get', imagePath);
        return !!result;
    }

    async function toggleFavorite(imagePath) {
        if (typeof WailsBridge !== 'undefined' && WailsBridge.isWails()) {
            const result = await WailsBridge.toggleFavorite(imagePath);
            return result.isFavorite;
        }
        if (serverAvailable) {
            const favs = serverDataCache.favorites || [];
            const idx = favs.indexOf(imagePath);
            if (idx >= 0) {
                favs.splice(idx, 1);
                serverDataCache.favorites = favs;
                markDirty();
                return false;
            } else {
                favs.push(imagePath);
                serverDataCache.favorites = favs;
                markDirty();
                return true;
            }
        }
        const store = getStore('favorites', 'readwrite');
        const existing = await promisify(store, 'get', imagePath);
        if (existing) {
            await promisify(store, 'delete', imagePath);
            return false;
        } else {
            await promisify(store, 'add', { imagePath, favoritedAt: new Date().toISOString() });
            return true;
        }
    }

    async function getAllFavorites() {
        if (typeof WailsBridge !== 'undefined' && WailsBridge.isWails()) {
            return await WailsBridge.getAllFavorites();
        }
        if (serverAvailable) {
            return serverDataCache.favorites || [];
        }
        const store = getStore('favorites');
        const all = await promisify(store, 'getAll');
        return all.map(f => f.imagePath);
    }

    async function setFavorite(imagePath, value) {
        if (typeof WailsBridge !== 'undefined' && WailsBridge.isWails()) {
            await WailsBridge.setFavorite(imagePath, value);
            return;
        }
        if (serverAvailable) {
            const favs = serverDataCache.favorites || [];
            if (value) {
                if (!favs.includes(imagePath)) {
                    favs.push(imagePath);
                    serverDataCache.favorites = favs;
                    markDirty();
                }
            } else {
                const idx = favs.indexOf(imagePath);
                if (idx >= 0) {
                    favs.splice(idx, 1);
                    serverDataCache.favorites = favs;
                    markDirty();
                }
            }
            return;
        }
        const store = getStore('favorites', 'readwrite');
        if (value) {
            await promisify(store, 'put', { imagePath, favoritedAt: new Date().toISOString() });
        } else {
            await promisify(store, 'delete', imagePath);
        }
    }

    // ==================== API 配置操作 ====================

    async function getAllApiConfigs() {
        if (serverAvailable) {
            return serverDataCache.apiConfigs || [];
        }
        const store = getStore('apiConfigs');
        return await promisify(store, 'getAll');
    }

    async function addApiConfig(config) {
        if (serverAvailable) {
            config.id = config.id || 'api_' + Date.now();
            config.createdAt = config.createdAt || new Date().toISOString();
            if (config.isDefault) {
                serverDataCache.apiConfigs.forEach(c => c.isDefault = false);
            }
            serverDataCache.apiConfigs.push(config);
            markDirty();
            return config;
        }
        const store = getStore('apiConfigs', 'readwrite');
        config.id = config.id || 'api_' + Date.now();
        config.createdAt = config.createdAt || new Date().toISOString();
        if (config.isDefault) {
            const all = await promisify(store, 'getAll');
            for (const c of all) {
                if (c.isDefault) {
                    c.isDefault = false;
                    await promisify(store, 'put', c);
                }
            }
        }
        await promisify(store, 'add', config);
        return config;
    }

    async function updateApiConfig(config) {
        if (serverAvailable) {
            if (config.isDefault) {
                serverDataCache.apiConfigs.forEach(c => {
                    if (c.id !== config.id) c.isDefault = false;
                });
            }
            const idx = serverDataCache.apiConfigs.findIndex(c => c.id === config.id);
            if (idx !== -1) serverDataCache.apiConfigs[idx] = config;
            markDirty();
            return config;
        }
        const store = getStore('apiConfigs', 'readwrite');
        if (config.isDefault) {
            const all = await promisify(store, 'getAll');
            for (const c of all) {
                if (c.id !== config.id && c.isDefault) {
                    c.isDefault = false;
                    await promisify(store, 'put', c);
                }
            }
        }
        await promisify(store, 'put', config);
        return config;
    }

    async function deleteApiConfig(configId) {
        if (serverAvailable) {
            serverDataCache.apiConfigs = serverDataCache.apiConfigs.filter(c => c.id !== configId);
            markDirty();
            return;
        }
        const store = getStore('apiConfigs', 'readwrite');
        await promisify(store, 'delete', configId);
    }

    async function getDefaultApiConfig() {
        // 优先使用当前在下拉框中选中的配置（从 settings 读取）
        const activeId = serverAvailable
            ? (serverDataCache.settings && serverDataCache.settings.activeApiConfigId)
            : (await getSetting('activeApiConfigId', null));
        const preferActive = (list) => {
            if (activeId) {
                const active = list.find(c => c.id === activeId);
                if (active) return active;
            }
            return list.find(c => c.isDefault) || list[0] || null;
        };
        if (serverAvailable) {
            return preferActive(serverDataCache.apiConfigs);
        }
        const store = getStore('apiConfigs');
        const all = await promisify(store, 'getAll');
        return preferActive(all);
    }

    // ==================== 提示词版本操作 ====================

    async function getPromptVersions(imagePath) {
        if (serverAvailable) {
            if (typeof WailsBridge !== 'undefined' && WailsBridge.isWails()) {
                const result = await WailsBridge.getPromptVersions(imagePath);
                return result || [];
            }
            // HTTP 模式：从内存缓存读取
            const versions = serverDataCache.promptVersions || [];
            return versions.filter(p => p.imagePath === imagePath).sort((a, b) => (a.createdAt || '').localeCompare(b.createdAt || ''));
        }
        const store = getStore('promptVersions');
        const all = await promisify(store, 'getAll');
        return all.filter(p => p.imagePath === imagePath).sort((a, b) => a.createdAt.localeCompare(b.createdAt));
    }

    async function addPromptVersion(imagePath, promptData) {
        if (serverAvailable && typeof WailsBridge !== 'undefined' && WailsBridge.isWails()) {
            const result = await WailsBridge.addPromptVersion(
                imagePath,
                promptData.positivePrompt || '',
                promptData.negativePrompt || '',
                promptData.source || 'custom'
            );
            if (result && result.success && result.version) {
                Gallery.refreshPromptCounts();
                return result.version;
            }
            throw new Error((result && result.error) || '添加提示词版本失败');
        }
        if (serverAvailable) {
            // HTTP 模式：内存缓存
            const version = {
                id: 'pv_' + Date.now() + '_' + Math.random().toString(36).substr(2, 6),
                imagePath,
                positivePrompt: promptData.positivePrompt || '',
                negativePrompt: promptData.negativePrompt || '',
                source: promptData.source || 'custom',
                createdAt: new Date().toISOString()
            };
            if (!serverDataCache.promptVersions) serverDataCache.promptVersions = [];
            serverDataCache.promptVersions.push(version);
            markDirty();
            return version;
        }
        const store = getStore('promptVersions', 'readwrite');
        const version = {
            imagePath,
            positivePrompt: promptData.positivePrompt || '',
            negativePrompt: promptData.negativePrompt || '',
            source: promptData.source || 'custom',
            createdAt: new Date().toISOString()
        };
        const id = await promisify(store, 'add', version);
        version.id = id;
        return version;
    }

    async function deletePromptVersion(versionId) {
        if (serverAvailable && typeof WailsBridge !== 'undefined' && WailsBridge.isWails()) {
            await WailsBridge.deletePromptVersion(versionId);
            Gallery.refreshPromptCounts();
            return;
        }
        if (serverAvailable) {
            if (!serverDataCache.promptVersions) serverDataCache.promptVersions = [];
            serverDataCache.promptVersions = serverDataCache.promptVersions.filter(p => p.id !== versionId);
            markDirty();
            return;
        }
        const store = getStore('promptVersions', 'readwrite');
        await promisify(store, 'delete', versionId);
    }

    async function updatePromptVersion(version) {
        if (serverAvailable) {
            if (!serverDataCache.promptVersions) serverDataCache.promptVersions = [];
            const idx = serverDataCache.promptVersions.findIndex(p => p.id === version.id);
            if (idx !== -1) serverDataCache.promptVersions[idx] = version;
            markDirty();
            return version;
        }
        const store = getStore('promptVersions', 'readwrite');
        await promisify(store, 'put', version);
        return version;
    }

    async function getAllPromptVersionCounts() {
        if (serverAvailable && typeof WailsBridge !== 'undefined' && WailsBridge.isWails()) {
            const result = await WailsBridge.getAllPromptVersionCounts();
            return result || {};
        }
        const counts = {};
        let allVersions = [];
        if (serverAvailable) {
            allVersions = serverDataCache.promptVersions || [];
        } else {
            const store = getStore('promptVersions');
            allVersions = await promisify(store, 'getAll');
        }
        for (const v of allVersions) {
            const path = v.imagePath;
            counts[path] = (counts[path] || 0) + 1;
        }
        return counts;
    }

    // ==================== 设置操作 ====================

    async function getSetting(key, defaultValue = null) {
        if (serverAvailable) {
            return serverDataCache.settings[key] !== undefined ? serverDataCache.settings[key] : defaultValue;
        }
        const store = getStore('settings');
        const result = await promisify(store, 'get', key);
        return result ? result.value : defaultValue;
    }

    async function setSetting(key, value) {
        if (serverAvailable) {
            serverDataCache.settings[key] = value;
            markDirty();
            return;
        }
        const store = getStore('settings', 'readwrite');
        await promisify(store, 'put', { key, value });
    }

    // ==================== 批量导入/导出 ====================

    async function exportAllData() {
        const tags = await getAllTags();
        let imageTags;
        if (typeof WailsBridge !== 'undefined' && WailsBridge.isWails()) {
            imageTags = await WailsBridge.getAllImageTags();
        } else if (serverAvailable) {
            imageTags = [];
        } else {
            imageTags = await promisify(getStore('imageTags'), 'getAll');
        }
        const favorites = await getAllFavorites();
        const apiConfigs = await getAllApiConfigs();
        let promptVersions;
        if (typeof WailsBridge !== 'undefined' && WailsBridge.isWails()) {
            promptVersions = await WailsBridge.getAllPromptVersions();
        } else if (serverAvailable) {
            promptVersions = [];
        } else {
            promptVersions = await promisify(getStore('promptVersions'), 'getAll');
        }
        const settings = serverAvailable ? serverDataCache.settings : await promisify(getStore('settings'), 'getAll');

        return {
            version: 1,
            exportedAt: new Date().toISOString(),
            tags,
            imageTags,
            favorites,
            apiConfigs,
            promptVersions,
            settings
        };
    }

    async function importAllData(data) {
        if (!data || data.version !== 1) {
            throw new Error('无效的导入数据格式');
        }

        if (serverAvailable) {
            if (data.registeredRoots && typeof WailsBridge !== 'undefined' && WailsBridge.isWails()) {
                const roots = data.registeredRoots.map(p => ({path: p, name: p.split(/[\\/]/).pop(), displayName: '', folderType: '', handleName: '', addedAt: new Date().toISOString()}));
                await WailsBridge.saveRootsWithMeta(roots);
            }
            if (data.tags) serverDataCache.tags = data.tags;
            if (data.imageTags && data.imageTags.length > 0 && typeof WailsBridge !== 'undefined' && WailsBridge.isWails()) {
                await WailsBridge.importImageTags(data.imageTags);
            }
            if (data.favorites && data.favorites.length > 0 && typeof WailsBridge !== 'undefined' && WailsBridge.isWails()) {
                for (const f of data.favorites) {
                    const p = typeof f === 'string' ? f : f.imagePath || f;
                    if (p) await WailsBridge.setFavorite(p, true);
                }
            }
            if (data.apiConfigs) serverDataCache.apiConfigs = data.apiConfigs;
            if (data.settings) serverDataCache.settings = data.settings;
            if (data.promptVersions && data.promptVersions.length > 0 && typeof WailsBridge !== 'undefined' && WailsBridge.isWails()) {
                await WailsBridge.importPromptVersions(data.promptVersions);
            }
            await saveToServer();
            return;
        }

        if (data.tags && data.tags.length > 0) {
            const tagStore = getStore('tags', 'readwrite');
            for (const tag of data.tags) {
                await promisify(tagStore, 'put', tag);
            }
        }

        if (data.imageTags && data.imageTags.length > 0) {
            const itStore = getStore('imageTags', 'readwrite');
            for (const it of data.imageTags) {
                try { await promisify(itStore, 'put', it); } catch (e) { /* 忽略重复 */ }
            }
        }

        if (data.favorites && data.favorites.length > 0) {
            const favStore = getStore('favorites', 'readwrite');
            for (const fav of data.favorites) {
                const path = typeof fav === 'string' ? fav : fav.imagePath;
                await promisify(favStore, 'put', { imagePath: path, favoritedAt: new Date().toISOString() });
            }
        }

        if (data.apiConfigs && data.apiConfigs.length > 0) {
            const apiStore = getStore('apiConfigs', 'readwrite');
            for (const config of data.apiConfigs) {
                await promisify(apiStore, 'put', config);
            }
        }

        if (data.promptVersions && data.promptVersions.length > 0) {
            const promptStore = getStore('promptVersions', 'readwrite');
            for (const pv of data.promptVersions) {
                await promisify(promptStore, 'put', pv);
            }
        }

        if (data.settings && data.settings.length > 0) {
            const settingsStore = getStore('settings', 'readwrite');
            for (const s of data.settings) {
                await promisify(settingsStore, 'put', s);
            }
        }
    }

    // ==================== 公开 API ====================

    return {
        init,
        syncFromServer,
        saveToServer,
        isServerAvailable: () => serverAvailable,
        getRegisteredRoots,
        setRegisteredRoots,
        saveDirectoryHandle,
        getAllDirectoryHandles,
        removeDirectoryHandle,
        clearDirectoryHandles,
        getAllTags, addTag, updateTag, deleteTag, getChildTags,
        getTagsForImage, addTagToImage, removeTagFromImage, getImagesForTag,
        isFavorite, toggleFavorite, getAllFavorites, setFavorite,
        getAllApiConfigs, addApiConfig, updateApiConfig, deleteApiConfig, getDefaultApiConfig,
        getPromptVersions, addPromptVersion, deletePromptVersion, updatePromptVersion, getAllPromptVersionCounts,
        getSetting, setSetting,
        exportAllData, importAllData
    };
})();
