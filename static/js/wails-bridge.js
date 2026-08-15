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

    // ★ 选择缩略图数据库文件（.db）——直接选文件而非文件夹，路径天然正确
    async function selectThumbDBFile() {
        if (!isWailsEnv) {
            throw new Error('仅桌面环境支持选择缩略图数据库文件');
        }
        return getApp().SelectThumbDBFile();
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

    // ★ 文件夹切换取消：通知后端"遗弃"离开的文件夹，取消其排队中的缩略图生成
    async function abandonFolder(folderPath) {
        if (!isWailsEnv || !folderPath) return;
        try {
            await getApp().AbandonFolder(folderPath);
        } catch (e) { console.warn('[worker] 通知遗弃失败:', e.message); }
    }

    // ★ 通知后端当前聚焦的文件夹（从遗弃集合恢复）
    async function focusFolder(folderPath) {
        if (!isWailsEnv || !folderPath) return;
        try {
            await getApp().FocusFolder(folderPath);
            console.log('[worker] 已通知后端聚焦文件夹:', folderPath);
        } catch (e) { console.warn('[worker] 通知聚焦失败:', e.message); }
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

    async function getFolderProgress(folderPath) {
        if (!isWailsEnv) {
            return { count: -1, thumbCount: 0 };
        }
        return getApp().GetFolderProgress(folderPath);
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
    let _httpBaseURL2 = ''; // 缩略图第二 origin（浏览器为每个 origin 分配独立 6 连接池）

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
        const url = await getApp().GetThumbBaseURL2();
        _httpBaseURL2 = url || '';
        return _httpBaseURL2;
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
        // ★ 归一化：本程序的图片/缩略图服务器端口每次启动都会变（127.0.0.1:随机端口）。
        //   先把"任意端口的完整链接"还原成相对路径（/image/、/thumb/），
        //   再统一补上当前端口，保证重启后已保存的旧链接（含旧端口）仍然可用。
        result = result.replace(/(https?:\/\/)(127\.0\.0\.1|localhost)(:\d+)?(\/image\/|\/thumb\/)/gi, '$4');
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

    /**
     * ★ HTML 标签 CSS 作用域化（供侧栏/详情/画廊/预览共用）。
     * 修复两个兼容性 bug：
     * 1. @keyframes 名全局冲突：不同标签用同名动画会互相覆盖。
     *    这里把 @keyframes 名加作用域后缀重命名，并同步替换 animation/animation-name 引用。
     * 2. `*` 通用选择器被直接删除：改为作用域化（[data-tag-scope] *），保留其效果。
     * html/body/:root 仍剔除，避免污染整个页面。
     */
    // scopeHtmlTagCss(raw, scopeId, fillMode)
    // fillMode=true（填满容器模式）：html/body/:root 视为"标签容器本身"，
    // 让整页式 CSS（html/body/scene 的 100% 高度链）在标签的固定盒子里成立。
    function scopeHtmlTagCss(raw, scopeId, fillMode) {
        if (!raw) return raw;

        // 0. 先剔除 CSS 注释：注释紧贴在 @keyframes/@media 前面时，会与 at 规则
        //    被规则分割正则当作同一个"选择器"处理，导致 @keyframes 名被整体当作
        //    普通选择器前缀化而语法损坏（对应动画静默失效）。注释不影响渲染语义。
        raw = raw.replace(/\/\*[\s\S]*?\*\//g, ' ');

        // 1. 收集并重命名 @keyframes（加作用域后缀，避免全局冲突）
        const kfMap = {}; // 原始名 → 作用域名
        let result = raw.replace(/@keyframes\s+([A-Za-z0-9_-]+)/g, (m, name) => {
            const scopedName = name + '-' + scopeId;
            kfMap[name] = scopedName;
            return '@keyframes ' + scopedName;
        });

        // 2. 替换 animation / animation-name 中引用的 keyframes 名
        const kfNames = Object.keys(kfMap);
        if (kfNames.length > 0) {
            result = result.replace(/animation\s*:\s*([^;{}]+)/g, (m, list) => {
                let out = list;
                for (const name of kfNames) {
                    out = out.replace(new RegExp('\\b' + name + '\\b', 'g'), kfMap[name]);
                }
                return 'animation: ' + out;
            });
            result = result.replace(/animation-name\s*:\s*([^;{}]+)/g, (m, v) => {
                let out = v;
                for (const name of kfNames) {
                    out = out.replace(new RegExp('\\b' + name + '\\b', 'g'), kfMap[name]);
                }
                return 'animation-name: ' + out;
            });
        }

        // 3. 作用域化普通规则（@规则与 keyframes 步骤保留；
        //    html/body/:root 也作用域化——在标签容器内永远不会匹配，既避免污染页面
        //    又保持 CSS 语法完整（不能直接剔除，否则会留下孤立的 {…} 破坏后续规则）；
        //    `*` 作用域化为 [data-tag-scope] *，保留其效果）
        result = result.replace(/([^{}]*\{)/g, (rule) => {
            const trimmed = rule.trim();
            if (/^@|^\d+(\.\d+)?%|^(from|to)\b/i.test(trimmed)) return rule;
            if (fillMode) {
                // 填满容器：html/body/:root → 容器本身；其余选择器前缀作用域
                // 注意：regex 匹配到的 rule 已含末尾 {，先去掉再统一补回
                const selectorPart = trimmed.replace(/\{\s*$/, '');
                return selectorPart.split(',').map((part) => {
                    const p = part.trim();
                    if (!p) return '';
                    if (/^(html|body|:root)$/.test(p)) {
                        return '[data-tag-scope="' + scopeId + '"]';
                    }
                    if (/^(html|body|:root)([\s>+~]|$)/.test(p)) {
                        return p.replace(/^(html|body|:root)/, '[data-tag-scope="' + scopeId + '"]');
                    }
                    return '[data-tag-scope="' + scopeId + '"] ' + p;
                }).filter(Boolean).join(', ') + '{';
            }
            return rule.replace(/(^|,)\s*/g, (sep) => {
                return sep + '[data-tag-scope="' + scopeId + '"] ';
            });
        });
        if (fillMode) {
            // ★ 填满容器模式：标签盒子就是"视口"。把 vh/vw/vmin 等视口单位换算成 %，
            //   否则 body 的 min-height:100vh 会被作用域化到容器上，把固定尺寸的标签框
            //   拉到整个窗口高（标签导航栏被撑爆）。换算成 % 后相对容器解析（容器尺寸固定）。
            result = result.replace(/(\d*\.?\d+)(dvh|dvw|svh|svw|lvh|lvw|vmin|vmax|vh|vw)\b/gi, '$1%');

            // ★ 保证标签盒子本身透明：去掉"容器本身"（html/body/:root 映射到的
            //   [data-tag-scope] 裸选择器）所在规则里的背景类声明。
            //   页面的 body 背景是为整页视口设计的，作用到固定尺寸的标签盒上后，
            //   会从内容边缘（fit 安全边距 + 百分比留白，如 width:92vw 的留白）露出，
            //   变成图片周围的黑/暗框。内容元素（如 .scene/.wrapper）自己的背景不受影响。
            //   用"裸选择器后跟 , 或 {" 判断容器本身，兼容 body, .foo { } 这类分组规则。
            result = result.replace(/([^{}]+)\{([^{}]*)\}/g, (m, sel, decls) => {
                // 判断选择器里是否有"容器本身"（裸 [data-tag-scope]，后跟逗号或行尾），
                // 兼容 body, .foo { } 这类分组规则
                if (!/(?:^|,)\s*\[data-tag-scope="[^"]+"\]\s*(?=,|$)/.test(sel)) return m;
                if (!/background/i.test(decls)) return m;
                const cleaned = decls.replace(/background(?:-[a-z-]+)?\s*:\s*((?:[^;()]|\([^)]*\))*);/gi, '');
                return sel + '{' + cleaned + '}';
            });
        }
        return result;
    }

    /**
     * ★ 填满容器模式的壁纸式缩放（类似 background-size: cover）：
     * 1. 先测量内容的世界范围，把 inner 撑开到世界尺寸（scene 的 100% 高度变成世界大小，背景铺满）；
     * 2. 布局稳定后：
     *    - 缩放：按整个世界范围 cover 缩放（保证背景铺满标签框、允许放大、超出裁切）；
     *    - 居中：以"视觉主体"为准（排除直接铺满容器的背景层，如 .scene），
     *      避免主体元素定位偏下/偏上（如 top:55%）时在标签框里留下大片空白。
     * @param {HTMLElement} inner - 填满容器(100%)的内容元素
     * @param {number} boxW - 标签框宽
     * @param {number} boxH - 标签框高
     */
    function fitHtmlTagContent(inner, boxW, boxH) {
        const MARGIN = 0.06; // 安全边距（适配亚像素/四舍五入）
        // 测量世界范围（排除"横条装饰"——无限滚动背景条是设计上可被裁切的）
        const measureWorld = () => {
            const box = inner.getBoundingClientRect();
            let minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity;
            inner.querySelectorAll('*').forEach(el => {
                const r = el.getBoundingClientRect();
                if (!(r.width > 0) || !(r.height > 0)) return;
                if (r.width / r.height > 3) return; // 横条装饰排除（如 200% 宽的滚动地面）
                const rx = r.left - box.left;
                const ry = r.top - box.top;
                if (rx < minX) minX = rx;
                if (ry < minY) minY = ry;
                if (r.right - box.left > maxX) maxX = r.right - box.left;
                if (r.bottom - box.top > maxY) maxY = r.bottom - box.top;
            });
            return { minX, minY, maxX, maxY, w: maxX - minX, h: maxY - minY };
        };
        // 测量时暂时清除 transform（得到未变换的世界坐标），测完恢复
        const measure = () => {
            const saved = inner.style.transform;
            inner.style.transform = 'none';
            const e = measureWorld();
            inner.style.transform = saved;
            return e;
        };
        // 按范围铺满标签框（非均匀拉伸 + 居中）
        const applyFit = (ext) => {
            if (!isFinite(ext.w) || !isFinite(ext.h) || ext.w <= 0 || ext.h <= 0) return;
            const scaleX = boxW / (ext.w * (1 + MARGIN));
            const scaleY = boxH / (ext.h * (1 + MARGIN));
            const cx = ext.minX + ext.w / 2;
            const cy = ext.minY + ext.h / 2;
            inner.style.transformOrigin = '0 0';
            inner.style.transform = `translate(${boxW / 2 - cx * scaleX}px, ${boxH / 2 - cy * scaleY}px) scale(${scaleX}, ${scaleY})`;
        };
        // ★ 通过代码计算动画的完整边界：
        //   用 Web Animations API 把每个动画跳到每个关键帧，让浏览器渲染该帧，
        //   测量整个世界范围，取所有关键帧的并集 = 动画的精确最大边界。
        //   对任何纯 CSS @keyframes 动画（transform/opacity/margin/尺寸等）都成立。
        const measureWithAnimationExtremes = () => {
            let ext = measure();
            const anims = [];
            inner.querySelectorAll('*').forEach(el => {
                if (el.getAnimations) {
                    el.getAnimations().forEach(a => anims.push({
                        a,
                        wasRunning: a.playState === 'running',
                        origTime: a.currentTime
                    }));
                }
            });
            if (anims.length === 0) return ext;
            anims.forEach(({ a }) => { try { a.pause(); } catch (e) {} });
            for (const { a } of anims) {
                try {
                    const effect = a.effect;
                    if (!effect || !effect.getKeyframes) continue;
                    const kfs = effect.getKeyframes();
                    const timing = effect.getComputedTiming();
                    const dur = timing ? timing.duration : 0;
                    if (!kfs || kfs.length === 0 || typeof dur !== 'number' || !isFinite(dur) || dur <= 0) continue;
                    for (let i = 0; i < kfs.length; i++) {
                        const off = (kfs[i].offset !== undefined && kfs[i].offset !== null)
                            ? kfs[i].offset : (kfs.length > 1 ? i / (kfs.length - 1) : 0);
                        a.currentTime = off * dur;
                        const e = measure();
                        if (isFinite(e.w) && e.w > 0 && isFinite(e.h) && e.h > 0) {
                            ext.minX = Math.min(ext.minX, e.minX);
                            ext.minY = Math.min(ext.minY, e.minY);
                            ext.maxX = Math.max(ext.maxX, e.maxX);
                            ext.maxY = Math.max(ext.maxY, e.maxY);
                        }
                    }
                } catch (e) { /* 单个动画失败忽略 */ }
            }
            // ★ 恢复动画：只恢复原本就在运行的动画到原位再继续播放，
            //   避免 fit 反复暂停/重置把动画卡在起点或暂停态（表现为"动画不动"）。
            anims.forEach(({ a, wasRunning, origTime }) => {
                try {
                    a.currentTime = origTime;
                    if (wasRunning) { a.play(); } else { a.pause(); }
                } catch (e) {}
            });
            ext.w = ext.maxX - ext.minX;
            ext.h = ext.maxY - ext.minY;
            return ext;
        };

        // 固定点迭代：inner 尺寸 ↔ 百分比定位 ↔ 动画极端 互相影响。
        // 场景是 100%×100%（=inner），百分比定位的元素随 inner 尺寸移动，
        // 迭代撑开 inner 直到所有内容（含动画极端）都落在场景内，再铺满标签框。
        let innerW = inner.getBoundingClientRect().width || boxW;
        let innerH = inner.getBoundingClientRect().height || boxH;
        inner.style.width = innerW + 'px';
        inner.style.height = innerH + 'px';
        let ext = null;
        for (let iter = 0; iter < 8; iter++) {
            const e = measureWithAnimationExtremes();
            if (!isFinite(e.w) || e.w <= 0 || !isFinite(e.h) || e.h <= 0) { ext = e; break; }
            ext = e;
            // 内容（含动画极端）需要落在 0..innerW / 0..innerH 内，场景才不裁切
            const needW = Math.max(e.maxX, e.w);
            const needH = Math.max(e.maxY, e.h);
            const newW = Math.max(innerW, needW);
            const newH = Math.max(innerH, needH);
            if (Math.abs(newW - innerW) < 0.5 && Math.abs(newH - innerH) < 0.5) break; // 收敛
            innerW = newW;
            innerH = newH;
            inner.style.width = innerW + 'px';
            inner.style.height = innerH + 'px';
        }
        if (ext) applyFit(ext);
    }

    /**
     * ★ 填满容器模式的 fit + 图片/字体加载完成后自动重算。
     * 初次 fit 时图片往往还没加载（宽高为 0），会算出错误的缩放/位置
     * （内容被过度拉伸或偏移，看起来"效果奇怪"，需要手动调尺寸才会正常）。
     * 这里在图片加载完、字体 ready 后重新 fit，让预览/渲染自动恢复正常。
     * @param {HTMLElement} inner - 填满容器(100%)的内容元素
     * @param {number} boxW - 标签框宽
     * @param {number} boxH - 标签框高
     */
    function fitHtmlTagContentAfterImages(inner, boxW, boxH) {
        const doFit = () => {
            try { fitHtmlTagContent(inner, boxW, boxH); } catch (e) { /* 静默 */ }
        };
        doFit();
        const imgs = inner.querySelectorAll('img');
        if (imgs.length === 0) return;
        let pending = 0;
        imgs.forEach(img => {
            if (img.complete) return;
            pending++;
            const h = () => { if (--pending <= 0) doFit(); };
            img.addEventListener('load', h, { once: true });
            img.addEventListener('error', h, { once: true });
        });
        // 字体加载完成后也可能改变文字尺寸进而影响世界范围
        if (document.fonts && document.fonts.ready) {
            document.fonts.ready.then(() => { try { doFit(); } catch (e) {} });
        }
    }

    // ==================== HTML 标签图片引用 ====================

    // 匹配 HTML/CSS 中的图片引用：<img src="X"> / <image href="X"> / url(X)
    // 组: img(1=前,2=值,3=后引号) image(4=前,5=值,6=后引号) url(7=前,8=值,9=后)
    const IMG_REF_RE = /(<img\b[^>]*?\bsrc\s*=\s*["'])([^"']*)(["'])|(<image\b[^>]*?\bhref\s*=\s*["'])([^"']*)(["'])|(url\(\s*["']?)([^"')]*)(["']?\s*\))/gi;

    /**
     * ★ 检测 HTML/CSS 中的图片引用（按文档顺序）。
     * @param {string} html - HTML 标签代码
     * @returns {Array<{type:string,value:string}>} 引用列表
     */
    function findImageRefs(html) {
        const refs = [];
        if (!html) return refs;
        IMG_REF_RE.lastIndex = 0;
        let m;
        while ((m = IMG_REF_RE.exec(html)) !== null) {
            if (m[1] !== undefined) refs.push({ type: 'img', value: m[2] });
            else if (m[4] !== undefined) refs.push({ type: 'image', value: m[5] });
            else refs.push({ type: 'url', value: m[8] });
        }
        return refs;
    }

    /**
     * ★ 把 url 数组按文档顺序替换进 HTML 的图片引用（与 findImageRefs 顺序一致）。
     * 用于 HTML 标签：编辑器里检测到 N 个图片引用 → 用户填 N 个链接 → 渲染时替换。
     * @param {string} html - HTML 标签代码
     * @param {string[]} urls - 用户填写的图片链接数组
     * @returns {string}
     */
    function substituteImageRefs(html, urls) {
        if (!html || !urls || urls.length === 0) return html;
        let i = 0;
        IMG_REF_RE.lastIndex = 0;
        return html.replace(IMG_REF_RE, (m, g1, g2, g3, g4, g5, g6, g7, g8, g9) => {
            if (i >= urls.length) return m;
            const url = urls[i++];
            if (g1 !== undefined) return g1 + url + g3;
            if (g4 !== undefined) return g4 + url + g6;
            return g7 + url + g9;
        });
    }

    /**
     * ★ 准备 HTML 标签的最终代码：先替换图片引用链接，再修复相对 URL。
     * 供侧栏/详情/画廊/预览共用。
     * @param {string} htmlCode - 用户输入的 HTML 代码
     * @param {string[]} [htmlImageUrls] - 用户填写的图片链接
     * @returns {string}
     */
    function prepareHtmlTagCode(htmlCode, htmlImageUrls) {
        let code = htmlCode || '';
        if (htmlImageUrls && htmlImageUrls.length > 0) {
            code = substituteImageRefs(code, htmlImageUrls);
        }
        return (typeof WailsBridge !== 'undefined' && WailsBridge.fixRelativeUrls)
            ? WailsBridge.fixRelativeUrls(code) : code;
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

    // ==================== 生成参数标签 ====================

    // 获取生成参数标签面板数据（按参数类别分组汇总取值标签）。force=true 强制重新聚合。
    async function getParamTags(force) {
        if (!isWailsEnv) {
            return { success: false, message: '浏览器环境不支持' };
        }
        return getApp().GetParamTags(!!force);
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
        selectThumbDBFile,
        scanFolder,
        refreshAll,
        rescanFolder,
        fullRescanFolder,
        removeFolder,
        removeImages,
        refresh,
        refreshFolder,
        abandonFolder,
        focusFolder,
        getImages,
        getImagesByPaths,
        getFolders,
        getFolderCount,
        getFolderProgress,
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
        scopeHtmlTagCss,
        fitHtmlTagContent,
        fitHtmlTagContentAfterImages,
        findImageRefs,
        substituteImageRefs,
        prepareHtmlTagCode,
        searchImages,
        advancedSearch,
        getParamTags,
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