axios.defaults.headers.post['Content-Type'] = 'application/x-www-form-urlencoded; charset=UTF-8';
axios.defaults.headers.common['X-Requested-With'] = 'XMLHttpRequest';

axios.interceptors.request.use(
    (config) => {
        // A FormData body must keep the browser-generated multipart boundary.
        // Setting Content-Type to a bare "multipart/form-data" (or leaving the
        // default application/x-www-form-urlencoded) makes Gin unable to parse
        // the file, so Restore Database silently fails.
        if (typeof FormData !== 'undefined' && config.data instanceof FormData) {
            const strip = (h) => {
                if (!h || typeof h !== 'object') return;
                if (typeof h.delete === 'function') {
                    h.delete('Content-Type');
                    h.delete('content-type');
                } else {
                    delete h['Content-Type'];
                    delete h['content-type'];
                }
                if (h.common && h.common !== h) strip(h.common);
                if (h.post && h.post !== h) strip(h.post);
            };
            strip(config.headers);
            return config;
        }
        const method = (config.method || 'get').toLowerCase();
        if (method === 'get' || method === 'head' || config.data == null) {
            return config;
        }
        config.data = Qs.stringify(config.data, {
            arrayFormat: 'repeat',
        });
        return config;
    },
    (error) => Promise.reject(error),
);

axios.interceptors.response.use(
    (response) => response,
    (error) => {
        if (error.response) {
            const statusCode = error.response.status;
            // Check the status code
            if (statusCode === 401) { // Unauthorized
                return window.location.reload();
            }
        }
        return Promise.reject(error);
    }
);
