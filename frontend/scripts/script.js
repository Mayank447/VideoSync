const BACKEND_URL = 'http://localhost:8080'; 

async function createSession() {
    try {
        const response = await fetch(`${BACKEND_URL}/api/sessions`, {
            method: 'POST'
        });
        
        if (!response.ok) throw new Error('Failed to create session');
        
        const { sessionKey } = await response.json();
        
        document.getElementById('sessionKey').textContent = sessionKey;
        document.getElementById('sessionKeyContainer').style.display = 'block';
        
        // Copy to clipboard
        navigator.clipboard.writeText(sessionKey).then(() => {
            alert('Session key copied to clipboard!');
        });
        
    } catch (error) {
        showError('Error creating session. Please try again.');
        console.error('Create session error:', error);
    }
}

async function joinSession() {
    const sessionKey = document.getElementById('joinKey').value.trim();
    const errorElement = document.getElementById('errorMessage');
    
    if (!sessionKey) {
        showError('Please enter a session key');
        return;
    }

    try {
        const response = await fetch(
            `${BACKEND_URL}/api/sessions/${encodeURIComponent(sessionKey)}/validate`
        );
        
        if (!response.ok) throw new Error('Invalid session key');
        
        const { valid } = await response.json();
        if (!valid) throw new Error('Invalid session key');
        
        window.location.href = `/pages/player.html?sessionKey=${encodeURIComponent(sessionKey)}`;
        
    } catch (error) {
        showError('Invalid session key. Please check and try again.');
        console.error('Join error:', error);
    }
}

function showError(message) {
    const errorElement = document.getElementById('errorMessage');
    errorElement.textContent = message;
    errorElement.style.display = 'block';
    setTimeout(() => {
        errorElement.style.display = 'none';
    }, 3000);
}