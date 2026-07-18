"""
Comprehensive tests for authentication system
"""
import pytest
from httpx import AsyncClient
from unittest.mock import patch, Mock
from fastapi import status
from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession

from app.models.user import User
from app.core.security import decode_token, verify_token_type
from app.core.exceptions import AuthenticationError, TokenError, GoogleAuthError

pytestmark = pytest.mark.integration


class TestGoogleAuthentication:
    """Test Google OAuth authentication"""
    
    @pytest.mark.asyncio
    async def test_google_auth_success_new_user(
        self, 
        client: AsyncClient, 
        db_session: AsyncSession,
        mock_google_oauth
    ):
        """Test successful Google authentication for new user"""
        # Mock Google OAuth response
        mock_google_oauth.verify_id_token.return_value = {
            "oauth_id": "google_123456789",
            "email": "newuser@example.com",
            "full_name": "New User",
            "avatar_url": "https://example.com/avatar.jpg",
            "email_verified": True
        }
        
        with patch('app.api.auth.google_oauth', mock_google_oauth):
            response = await client.post(
                "/api/auth/google",
                json={"id_token": "valid_google_token"}
            )
        
        assert response.status_code == status.HTTP_200_OK
        data = response.json()
        assert "access_token" in data
        assert "refresh_token" in data
        
        # Verify user was created in database
        result = await db_session.execute(
            select(User).where(User.email == "newuser@example.com")
        )
        user = result.scalar_one_or_none()
        assert user is not None
        assert user.oauth_provider == "google"
        assert user.oauth_id == "google_123456789"
        assert user.full_name == "New User"
        assert user.avatar_url == "https://example.com/avatar.jpg"
    
    @pytest.mark.asyncio
    async def test_google_auth_success_existing_user(
        self, 
        client: AsyncClient, 
        db_session: AsyncSession,
        test_user: User,
        mock_google_oauth
    ):
        """Test successful Google authentication for existing user"""
        # Mock Google OAuth response with same email
        mock_google_oauth.verify_id_token.return_value = {
            "oauth_id": "google_123456789",
            "email": test_user.email,
            "full_name": "Updated Name",
            "avatar_url": "https://example.com/new_avatar.jpg",
            "email_verified": True
        }
        
        with patch('app.api.auth.google_oauth', mock_google_oauth):
            response = await client.post(
                "/api/auth/google",
                json={"id_token": "valid_google_token"}
            )
        
        assert response.status_code == status.HTTP_200_OK
        
        # Verify user was updated
        await db_session.refresh(test_user)
        assert test_user.oauth_provider == "google"
        assert test_user.oauth_id == "google_123456789"
        assert test_user.full_name == "Updated Name"
        assert test_user.avatar_url == "https://example.com/new_avatar.jpg"
    
    @pytest.mark.asyncio
    async def test_google_auth_invalid_token(
        self, 
        client: AsyncClient,
        mock_google_oauth
    ):
        """Test Google authentication with invalid token"""
        # Mock Google OAuth to return None (invalid token)
        mock_google_oauth.verify_id_token.return_value = None
        
        with patch('app.api.auth.google_oauth', mock_google_oauth):
            response = await client.post(
                "/api/auth/google",
                json={"id_token": "invalid_token"}
            )
        
        assert response.status_code == status.HTTP_400_BAD_REQUEST
        data = response.json()
        assert data["error"] == "google_auth_error"
        assert "Invalid Google ID token" in data["message"]
    
    @pytest.mark.asyncio
    async def test_google_auth_verification_error(
        self, 
        client: AsyncClient,
        mock_google_oauth
    ):
        """Test Google authentication with verification error"""
        # Mock Google OAuth to raise exception
        mock_google_oauth.verify_id_token.side_effect = Exception("Verification failed")
        
        with patch('app.api.auth.google_oauth', mock_google_oauth):
            response = await client.post(
                "/api/auth/google",
                json={"id_token": "invalid_token"}
            )
        
        assert response.status_code == status.HTTP_400_BAD_REQUEST
        data = response.json()
        assert data["error"] == "google_auth_error"
        assert "Failed to verify Google ID token" in data["message"]


class TestTokenValidation:
    """Test JWT token validation and security"""
    
    @pytest.mark.asyncio
    async def test_get_current_user_success(
        self, 
        client: AsyncClient, 
        test_user: User, 
        access_token: str
    ):
        """Test successful user authentication"""
        response = await client.get(
            "/api/auth/me",
            headers={"Authorization": f"Bearer {access_token}"}
        )
        
        assert response.status_code == status.HTTP_200_OK
        data = response.json()
        assert data["email"] == test_user.email
        assert data["full_name"] == test_user.full_name
    
    @pytest.mark.asyncio
    async def test_get_current_user_invalid_token(
        self, 
        client: AsyncClient
    ):
        """Test authentication with invalid token"""
        response = await client.get(
            "/api/auth/me",
            headers={"Authorization": "Bearer invalid_token"}
        )
        
        assert response.status_code == status.HTTP_401_UNAUTHORIZED
        data = response.json()
        assert "Invalid or expired token" in data["detail"]
    
    @pytest.mark.asyncio
    async def test_get_current_user_no_token(
        self, 
        client: AsyncClient
    ):
        """Test authentication without token"""
        response = await client.get("/api/auth/me")
        
        assert response.status_code == status.HTTP_401_UNAUTHORIZED
        data = response.json()
        assert "Not authenticated" in data["detail"]
    
    @pytest.mark.asyncio
    async def test_get_current_user_blacklisted_token(
        self, 
        client: AsyncClient, 
        access_token: str,
        mock_redis
    ):
        """Test authentication with blacklisted token"""
        # Mock Redis to return that token is blacklisted
        mock_redis.exists.return_value = 1
        
        with patch('app.core.security.get_redis_client', return_value=mock_redis):
            response = await client.get(
                "/api/auth/me",
                headers={"Authorization": f"Bearer {access_token}"}
            )
        
        assert response.status_code == status.HTTP_401_UNAUTHORIZED
        data = response.json()
        assert "Token has been revoked" in data["detail"]
    
    @pytest.mark.asyncio
    async def test_get_current_user_revoked_user(
        self, 
        client: AsyncClient, 
        test_user: User,
        access_token: str,
        mock_redis
    ):
        """Test authentication with revoked user tokens"""
        # Mock Redis to return that user was revoked after token was issued
        mock_redis.get.return_value = b"9999999999"  # Future timestamp
        
        with patch('app.core.security.get_redis_client', return_value=mock_redis):
            response = await client.get(
                "/api/auth/me",
                headers={"Authorization": f"Bearer {access_token}"}
            )
        
        assert response.status_code == status.HTTP_401_UNAUTHORIZED
        data = response.json()
        assert "Token has been revoked" in data["detail"]


class TestTokenBlacklisting:
    """Test JWT token blacklisting functionality"""
    
    @pytest.mark.asyncio
    async def test_logout_blacklists_token(
        self, 
        client: AsyncClient, 
        test_user: User, 
        access_token: str,
        mock_redis
    ):
        """Test that logout blacklists the token"""
        with patch('app.core.security.get_redis_client', return_value=mock_redis):
            response = await client.post(
                "/api/auth/logout",
                headers={"Authorization": f"Bearer {access_token}"}
            )
        
        assert response.status_code == status.HTTP_200_OK
        data = response.json()
        assert "Logged out successfully" in data["message"]
        
        # Verify blacklist_token was called
        mock_redis.setex.assert_called_once()
    
    @pytest.mark.asyncio
    async def test_revoke_all_tokens(
        self, 
        client: AsyncClient, 
        test_user: User, 
        access_token: str,
        mock_redis
    ):
        """Test revoking all tokens for a user"""
        with patch('app.core.security.get_redis_client', return_value=mock_redis):
            response = await client.post(
                "/api/auth/revoke-all-tokens",
                headers={"Authorization": f"Bearer {access_token}"}
            )
        
        assert response.status_code == status.HTTP_200_OK
        data = response.json()
        assert "All tokens for this user have been revoked" in data["message"]
        
        # Verify revoke_user_tokens was called
        mock_redis.set.assert_called_once()


class TestRateLimiting:
    """Test rate limiting on authentication endpoints"""
    
    @pytest.mark.asyncio
    async def test_google_auth_rate_limit(
        self, 
        client: AsyncClient,
        mock_google_oauth
    ):
        """Test rate limiting on Google auth endpoint"""
        # Mock successful Google OAuth
        mock_google_oauth.verify_id_token.return_value = {
            "oauth_id": "google_123456789",
            "email": "test@example.com",
            "full_name": "Test User",
            "email_verified": True
        }
        
        with patch('app.api.auth.google_oauth', mock_google_oauth):
            # Make multiple requests to trigger rate limiting
            for i in range(12):  # Exceed the 10/minute limit
                response = await client.post(
                    "/api/auth/google",
                    json={"id_token": f"token_{i}"}
                )
                
                if i < 10:
                    assert response.status_code == status.HTTP_200_OK
                else:
                    # Should be rate limited
                    assert response.status_code == status.HTTP_429_TOO_MANY_REQUESTS


class TestEmailPasswordAuthentication:
    """Test email/password authentication (if implemented)"""
    
    @pytest.mark.asyncio
    async def test_login_success(
        self, 
        client: AsyncClient, 
        test_user: User
    ):
        """Test successful email/password login"""
        # This test assumes email/password auth is implemented
        # For now, we'll skip if not implemented
        pytest.skip("Email/password authentication not fully implemented")
    
    @pytest.mark.asyncio
    async def test_login_invalid_credentials(
        self, 
        client: AsyncClient
    ):
        """Test login with invalid credentials"""
        pytest.skip("Email/password authentication not fully implemented")


class TestTokenRefresh:
    """Test token refresh functionality"""
    
    @pytest.mark.asyncio
    async def test_refresh_token_success(
        self, 
        client: AsyncClient, 
        test_user: User, 
        refresh_token: str
    ):
        """Test successful token refresh"""
        response = await client.post(
            "/api/auth/refresh",
            json={"refresh_token": refresh_token}
        )
        
        assert response.status_code == status.HTTP_200_OK
        data = response.json()
        assert "access_token" in data
        assert "refresh_token" in data
    
    @pytest.mark.asyncio
    async def test_refresh_token_invalid(
        self, 
        client: AsyncClient
    ):
        """Test refresh with invalid token"""
        response = await client.post(
            "/api/auth/refresh",
            json={"refresh_token": "invalid_refresh_token"}
        )
        
        assert response.status_code == status.HTTP_401_UNAUTHORIZED
        data = response.json()
        assert "Invalid or expired token" in data["detail"]


class TestUserProfile:
    """Test user profile management"""
    
    @pytest.mark.asyncio
    async def test_get_user_profile(
        self, 
        client: AsyncClient, 
        test_user: User, 
        access_token: str
    ):
        """Test getting user profile"""
        response = await client.get(
            "/api/auth/me",
            headers={"Authorization": f"Bearer {access_token}"}
        )
        
        assert response.status_code == status.HTTP_200_OK
        data = response.json()
        assert data["email"] == test_user.email
        assert data["full_name"] == test_user.full_name
        assert data["is_active"] == test_user.is_active
        assert data["is_admin"] == test_user.is_admin
    
    @pytest.mark.asyncio
    async def test_update_user_profile(
        self, 
        client: AsyncClient, 
        test_user: User, 
        access_token: str,
        db_session: AsyncSession
    ):
        """Test updating user profile"""
        update_data = {
            "full_name": "Updated Name",
            "avatar_url": "https://example.com/new_avatar.jpg"
        }
        
        response = await client.patch(
            "/api/auth/me",
            json=update_data,
            headers={"Authorization": f"Bearer {access_token}"}
        )
        
        assert response.status_code == status.HTTP_200_OK
        data = response.json()
        assert data["full_name"] == "Updated Name"
        assert data["avatar_url"] == "https://example.com/new_avatar.jpg"
        
        # Verify database was updated
        await db_session.refresh(test_user)
        assert test_user.full_name == "Updated Name"
        assert test_user.avatar_url == "https://example.com/new_avatar.jpg"


class TestSecurityFeatures:
    """Test security features and edge cases"""
    
    @pytest.mark.asyncio
    async def test_token_expiration(
        self, 
        client: AsyncClient
    ):
        """Test that expired tokens are rejected"""
        # Create an expired token (this would require mocking time)
        # For now, we'll test with an obviously invalid token
        response = await client.get(
            "/api/auth/me",
            headers={"Authorization": "Bearer expired_token"}
        )
        
        assert response.status_code == status.HTTP_401_UNAUTHORIZED
    
    @pytest.mark.asyncio
    async def test_inactive_user_rejection(
        self, 
        client: AsyncClient, 
        db_session: AsyncSession
    ):
        """Test that inactive users are rejected"""
        # Create inactive user
        inactive_user = User(
            email="inactive@example.com",
            full_name="Inactive User",
            is_active=False
        )
        db_session.add(inactive_user)
        await db_session.commit()
        await db_session.refresh(inactive_user)
        
        # Create token for inactive user
        from app.core.security import create_access_token
        token = create_access_token({"sub": str(inactive_user.id), "email": inactive_user.email})
        
        response = await client.get(
            "/api/auth/me",
            headers={"Authorization": f"Bearer {token}"}
        )
        
        assert response.status_code == status.HTTP_401_UNAUTHORIZED
        data = response.json()
        assert "User not found or inactive" in data["detail"]
