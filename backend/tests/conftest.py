"""
Pytest configuration and fixtures for RehearseKit backend tests
"""
import pytest
import asyncio
from typing import AsyncGenerator, Generator
from sqlalchemy.ext.asyncio import AsyncSession, create_async_engine, async_sessionmaker
from sqlalchemy.pool import StaticPool
from httpx import AsyncClient
from fastapi import FastAPI
from unittest.mock import Mock, AsyncMock

from app.main import app
from app.api.auth import limiter as auth_limiter
from app.core.database import get_db, Base
from app.core.config import settings
from app.models.user import User
from app.core.security import create_access_token, create_refresh_token


# Test database URL (in-memory SQLite for testing)
TEST_DATABASE_URL = "sqlite+aiosqlite:///:memory:"

# Create test engine
test_engine = create_async_engine(
    TEST_DATABASE_URL,
    poolclass=StaticPool,
    connect_args={"check_same_thread": False},
    echo=False,
)

TestSessionLocal = async_sessionmaker(
    test_engine, class_=AsyncSession, expire_on_commit=False
)


@pytest.fixture(scope="session")
def event_loop() -> Generator:
    """Create an instance of the default event loop for the test session."""
    loop = asyncio.get_event_loop_policy().new_event_loop()
    yield loop
    loop.close()


@pytest.fixture(autouse=True)
def reset_rate_limiters():
    """Keep rate-limit counters isolated between tests."""
    app.state.limiter.reset()
    auth_limiter.reset()
    yield
    app.state.limiter.reset()
    auth_limiter.reset()


@pytest.fixture(scope="function")
async def db_session() -> AsyncGenerator[AsyncSession, None]:
    """Create a test database session."""
    # Create tables
    async with test_engine.begin() as conn:
        await conn.run_sync(Base.metadata.create_all)
    
    # Create session
    async with TestSessionLocal() as session:
        yield session
    
    # Drop tables after test
    async with test_engine.begin() as conn:
        await conn.run_sync(Base.metadata.drop_all)


@pytest.fixture(scope="function")
async def client(db_session: AsyncSession) -> AsyncGenerator[AsyncClient, None]:
    """Create a test client with database dependency override."""
    
    def override_get_db():
        return db_session
    
    app.dependency_overrides[get_db] = override_get_db
    
    async with AsyncClient(app=app, base_url="http://test") as ac:
        yield ac
    
    app.dependency_overrides.clear()


@pytest.fixture
async def test_user(db_session: AsyncSession) -> User:
    """Create a test user."""
    user = User(
        email="test@example.com",
        full_name="Test User",
        is_active=True,
        is_admin=False
    )
    db_session.add(user)
    await db_session.commit()
    await db_session.refresh(user)
    return user


@pytest.fixture
async def test_admin_user(db_session: AsyncSession) -> User:
    """Create a test admin user."""
    user = User(
        email="admin@example.com",
        full_name="Admin User",
        is_active=True,
        is_admin=True
    )
    db_session.add(user)
    await db_session.commit()
    await db_session.refresh(user)
    return user


@pytest.fixture
async def test_google_user(db_session: AsyncSession) -> User:
    """Create a test user with Google OAuth."""
    user = User(
        email="google@example.com",
        full_name="Google User",
        oauth_provider="google",
        oauth_id="google_123456789",
        is_active=True,
        is_admin=False
    )
    db_session.add(user)
    await db_session.commit()
    await db_session.refresh(user)
    return user


@pytest.fixture
def access_token(test_user: User) -> str:
    """Create a valid access token for test user."""
    return create_access_token({"sub": str(test_user.id), "email": test_user.email})


@pytest.fixture
def refresh_token(test_user: User) -> str:
    """Create a valid refresh token for test user."""
    return create_refresh_token({"sub": str(test_user.id)})


@pytest.fixture
def admin_access_token(test_admin_user: User) -> str:
    """Create a valid access token for admin user."""
    return create_access_token({"sub": str(test_admin_user.id), "email": test_admin_user.email})


@pytest.fixture
def mock_redis():
    """Mock Redis client for testing."""
    mock_redis = Mock()
    mock_redis.exists.return_value = 0  # Token not blacklisted by default
    mock_redis.setex.return_value = True
    mock_redis.set.return_value = True
    mock_redis.get.return_value = None
    return mock_redis


@pytest.fixture
def mock_google_oauth():
    """Mock Google OAuth service."""
    mock_oauth = Mock()
    mock_oauth.verify_id_token = AsyncMock()
    return mock_oauth
